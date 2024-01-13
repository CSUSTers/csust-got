package util

import (
	"os"
)

// CreateFileIfNotExist create file if not exist
func CreateFileIfNotExist(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// create file
		file, err := os.Create(path)
		if err != nil {
			return err
		}
		defer file.Close()
	}
	return nil
}
