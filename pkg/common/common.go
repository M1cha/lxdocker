package common

import (
	"fmt"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/term"
	"os"
	"syscall"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"gopkg.in/yaml.v3"
)

type RootfsMetadata struct {
	// these two combined let us check if we need to regenerate
	SpecDigest     v1.Hash
	OciImageDigest v1.Hash

	// cached rootfs hash for usi with the image server
	LxdImageDigest v1.Hash
	// path to the rootfs
	Filename string
}

func ReadRootfsMetaData(path string) (*RootfsMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open `%s`: %w", path, err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	var metadata RootfsMetadata

	err = decoder.Decode(&metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to parse `%s`: %w", path, err)
	}

	return &metadata, nil
}

func MakeLogger() *zap.SugaredLogger {
	if term.IsTerminal(syscall.Stderr) {
		config := zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		logger, _ := config.Build()

		return logger.Sugar()
	} else {
		logger, _ := zap.NewProduction()
		defer logger.Sync()

		return logger.Sugar()
	}
}
