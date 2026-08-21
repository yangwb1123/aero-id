package config

import (
	"fmt"
	"net/url"
	"strings"
)

func validateStorage(value StorageConfig) error {
	switch value.Type {
	case "local":
		if strings.TrimSpace(value.Local.Root) == "" {
			return fmt.Errorf("storage.local.root is required when storage.type=local")
		}
		return nil
	case "s3":
		if strings.TrimSpace(value.S3.Region) == "" {
			return fmt.Errorf("storage.s3.region is required when storage.type=s3")
		}
		if strings.TrimSpace(value.S3.Bucket) == "" {
			return fmt.Errorf("storage.s3.bucket is required when storage.type=s3")
		}
		if (strings.TrimSpace(value.S3.AccessKey) == "") != (strings.TrimSpace(value.S3.SecretKey) == "") {
			return fmt.Errorf("storage.s3.access_key and storage.s3.secret_key must be configured together")
		}
		if value.S3.Endpoint != "" {
			if err := validateStorageEndpoint(value.S3.Endpoint); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("storage.type must be 'local' or 's3'")
	}
}

func validateStorageEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("storage.s3.endpoint must be a valid URL without userinfo")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("storage.s3.endpoint must use http or https")
	}
	if parsed.Scheme != "https" && !isLocalEndpoint(parsed.Hostname()) {
		return fmt.Errorf("storage.s3.endpoint must use https outside localhost")
	}
	return nil
}
