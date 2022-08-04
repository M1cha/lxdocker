package main

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"go.uber.org/zap"

	simplestreams "github.com/lxc/lxd/shared/simplestreams"
	"pkg/common"
)

var log *zap.SugaredLogger
var imagesDir string

func logRequestHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Infof("%s %s %s\n", r.RemoteAddr, r.Method, r.URL)
		h.ServeHTTP(w, r)
	})
}

func internalError(w http.ResponseWriter, text string, format string, a ...any) {
	formatResult := fmt.Sprintf(format, a...)

	log.Errorf("%s%s", formatResult)

	http.Error(w, text, http.StatusInternalServerError)
}

func indexJsonHandler(w http.ResponseWriter, r *http.Request) {
	var products []string

	files, err := ioutil.ReadDir(imagesDir)
	if err != nil {
		internalError(w, "failed to open images dir", " `%s`: %v", imagesDir, err)
		return
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		ext := filepath.Ext(file.Name())
		if ext != ".meta" {
			continue
		}

		name := strings.TrimSuffix(filepath.Base(file.Name()), ext)

		products = append(products, name)
	}

	var stream = simplestreams.Stream{
		Format: "index:1.0",
		Index: map[string]simplestreams.StreamIndex{
			"images": simplestreams.StreamIndex{
				DataType: "image-downloads",
				Path:     "streams/v1/images.json",
				Products: products,
				Format:   "products:1.0",
			},
		},
	}

	json.NewEncoder(w).Encode(&stream)
}

func imagesJsonHandler(w http.ResponseWriter, r *http.Request) {
	var productMap = map[string]simplestreams.Product{}

	files, err := ioutil.ReadDir(imagesDir)
	if err != nil {
		internalError(w, "failed to open images dir", " `%s`: %v", imagesDir, err)
		return
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		ext := filepath.Ext(file.Name())
		if ext != ".meta" {
			continue
		}

		name := strings.TrimSuffix(filepath.Base(file.Name()), ext)
		metadataPath := filepath.Join(imagesDir, file.Name())

		metadataInfo, err := os.Stat(metadataPath)
		if err != nil {
			log.Errorf("failed to stat `%s`: %w", metadataPath, err)
			continue
		}

		metadata, err := common.ReadRootfsMetaData(metadataPath)
		if err != nil {
			log.Errorf("failed to read rootfs data from `%s`: %w", file.Name(), err)
			continue
		}

		rootfsInfo, err := os.Stat(filepath.Join(imagesDir, metadata.Filename))
		if err != nil {
			log.Errorf("failed to stat `%s`: %w", metadata.Filename, err)
			continue
		}

		versionName := fmt.Sprintf("%s_%s", metadataInfo.ModTime().Format("20060102"), metadata.LxdImageDigest.Hex[:12])
		productMap[name] = simplestreams.Product{
			Aliases:         fmt.Sprintf("%s/current/default,%s/current,%s", name, name, name),
			Architecture:    runtime.GOARCH,
			OperatingSystem: fmt.Sprintf("docker:%s", name),
			ReleaseTitle:    "latest",
			Versions: map[string]simplestreams.ProductVersion{
				versionName: simplestreams.ProductVersion{
					Items: map[string]simplestreams.ProductVersionItem{
						"lxd_combined.tar.gz": simplestreams.ProductVersionItem{
							FileType:   "lxd_combined.tar.gz",
							HashSha256: metadata.LxdImageDigest.Hex,
							Path:       filepath.Join("images", metadata.Filename),
							Size:       rootfsInfo.Size(),
						},
					},
				},
			},
		}
	}

	var products = simplestreams.Products{
		ContentID: "images",
		DataType:  "image-downloads",
		Format:    "products-1.0",
		Products:  productMap,
	}

	json.NewEncoder(w).Encode(&products)
}

func rootfsJsonHandler(w http.ResponseWriter, r *http.Request) {
	pathComponents := strings.Split(r.URL.Path, "/")
	if len(pathComponents) != 3 || pathComponents[0] != "" || pathComponents[1] != "images" {
		log.Errorf("unsupported rootfs path: `%s`", r.URL.Path)
		http.Error(w, "", http.StatusNotFound)
		return
	}

	filename := pathComponents[2]

	ext := filepath.Ext(filename)
	if ext != ".rootfs" {
		log.Errorf("unsupported rootfs path: `%s`", r.URL.Path)
		http.Error(w, "", http.StatusNotFound)
		return
	}

	http.ServeFile(w, r, filepath.Join(imagesDir, filename))
}

func wildcardRequestHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/images/") {
			rootfsJsonHandler(w, r)
		} else {
			h.ServeHTTP(w, r)
		}
	})
}

func main() {
	log = common.MakeLogger()

	var address string
	var key string
	var cert string

	var rootCmd = &cobra.Command{
		Use:   "imgserver",
		Short: "serve lxdocker images to LXD",
		Run: func(cmd *cobra.Command, args []string) {
			http.HandleFunc("/streams/v1/index.json", indexJsonHandler)
			http.HandleFunc("/streams/v1/images.json", imagesJsonHandler)

			var handler http.Handler = http.DefaultServeMux
			handler = wildcardRequestHandler(handler)
			handler = logRequestHandler(handler)

			log.Infof("Start server at %s", address)
			err := http.ListenAndServeTLS(address, cert, key, handler)
			if err != nil {
				log.Fatalf("http listener returned: %w", err)
				return
			}
		},
	}

	rootCmd.Flags().StringVar(&address, "address", ":443", "server listener address")
	rootCmd.Flags().StringVar(&imagesDir, "lxdimages", "", "path to directory of generated LXD images")
	rootCmd.Flags().StringVar(&key, "key", "", "path to TLS key")
	rootCmd.Flags().StringVar(&cert, "cert", "", "path to TLS certificate")

	rootCmd.MarkFlagRequired("cache")
	rootCmd.MarkFlagRequired("lxdimages")
	rootCmd.MarkFlagRequired("key")
	rootCmd.MarkFlagRequired("cert")

	rootCmd.Execute()
}
