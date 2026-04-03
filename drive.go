package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const indexFileName = ".index.json"

// driveWriter handles all Google Drive file operations.
type driveWriter struct {
	svc          *drive.Service
	rootFolderID string
	folderCache  map[string]string // relative subdir path → Drive folder ID
}

func newDriveWriter(ctx context.Context, clientID, clientSecret, refreshToken, rootFolderID string) (*driveWriter, error) {
	cfg := oauthConfig(clientID, clientSecret, "")
	token := &oauth2.Token{RefreshToken: refreshToken}
	tokenSource := cfg.TokenSource(ctx, token)

	svc, err := drive.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		return nil, fmt.Errorf("create drive service: %w", err)
	}
	return &driveWriter{
		svc:          svc,
		rootFolderID: rootFolderID,
		folderCache:  make(map[string]string),
	}, nil
}

// WriteFile creates or overwrites a file at relPath (e.g. "Personal/2026-04-03_note.md").
// Returns the Drive file ID.
func (d *driveWriter) WriteFile(relPath string, content []byte) (string, error) {
	dir := filepath.Dir(relPath)
	name := filepath.Base(relPath)

	parentID, err := d.ensureFolder(dir)
	if err != nil {
		return "", fmt.Errorf("ensure folder %s: %w", dir, err)
	}

	existingID, err := d.findFile(name, parentID, false)
	if err != nil {
		return "", err
	}

	media := bytes.NewReader(content)

	if existingID != "" {
		_, err = d.svc.Files.Update(existingID, &drive.File{}).
			Media(media).
			Fields("id").
			Do()
		if err != nil {
			return "", fmt.Errorf("update file %s: %w", relPath, err)
		}
		return existingID, nil
	}

	f, err := d.svc.Files.Create(&drive.File{
		Name:     name,
		Parents:  []string{parentID},
		MimeType: "text/markdown",
	}).Media(media).Fields("id").Do()
	if err != nil {
		return "", fmt.Errorf("create file %s: %w", relPath, err)
	}
	return f.Id, nil
}

// DeleteFile removes a file by Drive ID.
func (d *driveWriter) DeleteFile(fileID string) error {
	return d.svc.Files.Delete(fileID).Do()
}

// LoadIndex fetches and parses the index file from the root Drive folder.
// Returns an empty index if the file doesn't exist.
func (d *driveWriter) LoadIndex() (driveIndex, string, error) {
	fileID, err := d.findFile(indexFileName, d.rootFolderID, false)
	if err != nil {
		return nil, "", err
	}
	if fileID == "" {
		return make(driveIndex), "", nil
	}

	resp, err := d.svc.Files.Get(fileID).Download()
	if err != nil {
		return nil, "", fmt.Errorf("download index: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	var idx driveIndex
	return idx, fileID, json.Unmarshal(data, &idx)
}

// SaveIndex uploads the index to Drive, updating the existing file if indexFileID is set.
func (d *driveWriter) SaveIndex(idx driveIndex, indexFileID string) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	media := bytes.NewReader(data)

	if indexFileID != "" {
		_, err = d.svc.Files.Update(indexFileID, &drive.File{}).Media(media).Do()
		return err
	}

	_, err = d.svc.Files.Create(&drive.File{
		Name:     indexFileName,
		Parents:  []string{d.rootFolderID},
		MimeType: "application/json",
	}).Media(media).Do()
	return err
}

// ensureFolder resolves a relative path like "Personal" to a Drive folder ID,
// creating the folder if it doesn't exist.
func (d *driveWriter) ensureFolder(relDir string) (string, error) {
	if relDir == "." || relDir == "" {
		return d.rootFolderID, nil
	}

	if id, ok := d.folderCache[relDir]; ok {
		return id, nil
	}

	parts := strings.Split(filepath.ToSlash(relDir), "/")
	parentID := d.rootFolderID
	cumPath := ""

	for _, part := range parts {
		if cumPath != "" {
			cumPath += "/"
		}
		cumPath += part

		if id, ok := d.folderCache[cumPath]; ok {
			parentID = id
			continue
		}

		id, err := d.findFile(part, parentID, true)
		if err != nil {
			return "", err
		}
		if id == "" {
			f, err := d.svc.Files.Create(&drive.File{
				Name:     part,
				Parents:  []string{parentID},
				MimeType: "application/vnd.google-apps.folder",
			}).Fields("id").Do()
			if err != nil {
				return "", fmt.Errorf("create folder %s: %w", cumPath, err)
			}
			id = f.Id
		}

		d.folderCache[cumPath] = id
		parentID = id
	}

	return parentID, nil
}

// findFile searches for a file or folder by name within a parent folder.
func (d *driveWriter) findFile(name, parentID string, folderOnly bool) (string, error) {
	mimeFilter := ""
	if folderOnly {
		mimeFilter = " and mimeType='application/vnd.google-apps.folder'"
	}
	q := fmt.Sprintf("name='%s' and '%s' in parents and trashed=false%s",
		strings.ReplaceAll(name, "'", "\\'"), parentID, mimeFilter)

	list, err := d.svc.Files.List().Q(q).Fields("files(id)").PageSize(1).Do()
	if err != nil {
		return "", fmt.Errorf("search %q: %w", name, err)
	}
	if len(list.Files) == 0 {
		return "", nil
	}
	return list.Files[0].Id, nil
}
