package localauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxCredentialFileBytes = 64 << 10

func loadCredential(path string) (credentialRecord, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return credentialRecord{}, false, nil
		}
		return credentialRecord{}, false, fmt.Errorf("inspect local auth credentials: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return credentialRecord{}, false, errors.New("local auth credential path is not a regular file")
	}
	if info.Size() > maxCredentialFileBytes {
		return credentialRecord{}, false, errors.New("local auth credential file is too large")
	}

	file, err := os.Open(path)
	if err != nil {
		return credentialRecord{}, false, fmt.Errorf("read local auth credentials: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return credentialRecord{}, false, fmt.Errorf("inspect open local auth credentials: %w", err)
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return credentialRecord{}, false, errors.New("local auth credential changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCredentialFileBytes+1))
	if err != nil {
		return credentialRecord{}, false, fmt.Errorf("read local auth credentials: %w", err)
	}
	if len(data) > maxCredentialFileBytes {
		return credentialRecord{}, false, errors.New("local auth credential file is too large")
	}

	var record credentialRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return credentialRecord{}, false, fmt.Errorf("decode local auth credentials: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return credentialRecord{}, false, fmt.Errorf("decode local auth credentials: %w", err)
	}
	if err := record.validate(); err != nil {
		return credentialRecord{}, false, fmt.Errorf("validate local auth credentials: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		return credentialRecord{}, false, fmt.Errorf("secure local auth credentials: %w", err)
	}
	return record, true, nil
}

func saveCredential(path string, record credentialRecord) error {
	if err := record.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(path, data, 0o600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".bosstransfer-auth-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	closeWithError := func(cause error) error {
		if closeErr := temporary.Close(); cause == nil {
			return closeErr
		}
		return cause
	}

	if err := temporary.Chmod(mode); err != nil {
		return closeWithError(err)
	}
	if _, err := temporary.Write(data); err != nil {
		return closeWithError(err)
	}
	if err := temporary.Sync(); err != nil {
		return closeWithError(err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("credential file contains multiple JSON values")
	}
	return err
}
