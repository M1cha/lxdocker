package main

// TODO: write a cloud-init lxd container with this service(systemd cron, static-busybox, debian-update and image server)
// TODO: implement image server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"pkg/common"
	"runtime"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/match"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	lxdapi "github.com/lxc/lxd/shared/api"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

const whiteoutPrefix = ".wh."

var log *zap.SugaredLogger

func shellEscape(s string) string {
	s = strings.Replace(s, "\\", "\\\\", -1)
	s = strings.Replace(s, "\"", "\\\"", -1)
	s = strings.Replace(s, "$", "\\$", -1)

	return s
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func inWhiteoutDir(fileMap map[string]bool, file string) bool {
	for {
		if file == "" {
			break
		}
		dirname := filepath.Dir(file)
		if file == dirname {
			break
		}
		if val, ok := fileMap[dirname]; ok && val {
			return true
		}
		file = dirname
	}
	return false
}

func writeTarFile(tarWriter *tar.Writer, fileMap map[string]bool, header *tar.Header, contents io.Reader) error {
	// Some tools prepend everything with "./", so if we don't Clean the
	// name, we may have duplicate entries, which angers tar-split.
	// This removes trailing slashes which would confuse `filepath.Dir` as
	// well. https://github.com/google/go-containerregistry/pull/128
	header.Name = filepath.Clean(header.Name)
	// force PAX format to remove Name/Linkname length limit of 100 characters
	// required by USTAR and to not depend on internal tar package guess which
	// prefers USTAR over PAX
	header.Format = tar.FormatPAX

	basename := filepath.Base(header.Name)
	dirname := filepath.Dir(header.Name)
	tombstone := strings.HasPrefix(basename, whiteoutPrefix)
	if tombstone {
		basename = basename[len(whiteoutPrefix):]
	}

	// use name without (possible) whiteout prefix
	name := filepath.Join(dirname, basename)

	// check if we have seen value before
	if _, ok := fileMap[name]; ok {
		return nil
	}

	// check for a whited out parent directory
	if inWhiteoutDir(fileMap, name) {
		return nil
	}

	// mark file as handled. non-directory implicitly tombstones
	// any entries with a matching (or child) name
	fileMap[name] = tombstone || !(header.Typeflag == tar.TypeDir)
	if !tombstone {
		// LXD expects the rootfs in a subdirectory
		header.Name = filepath.Join("rootfs", header.Name)
		if header.Typeflag == tar.TypeLink {
			header.Linkname = filepath.Join("rootfs", header.Linkname)
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header: %w", err)
		}

		if header.Size > 0 {
			if _, err := io.CopyN(tarWriter, contents, header.Size); err != nil {
				return fmt.Errorf("failed to write tar contents: %w", err)
			}
		}
	}

	return nil
}

func writeInit(tarWriter *tar.Writer, fileMap map[string]bool, config *v1.Config) error {
	var data bytes.Buffer

	_, err := fmt.Fprintf(&data, "#!/busybox-lxd sh\n\n")
	check(err)

	for _, keyval := range config.Env {
		key := strings.Split(keyval, "=")[0]
		_, err = fmt.Fprintf(&data, "if [ -z \"${%s+x}\" ]; then export \"%s\"; fi\n", key, shellEscape(keyval))
		check(err)
	}

	_, err = fmt.Fprintf(&data, "cd \"%v\"\n", shellEscape(config.WorkingDir))
	check(err)

	// some containers need shared memory
	_, err = fmt.Fprintf(&data, "/busybox-lxd mkdir -p /dev/shm\n")
	check(err)
	_, err = fmt.Fprintf(&data, "/busybox-lxd mount -t tmpfs shmfs /dev/shm\n")
	check(err)

	// containers rarely need to be routers
	_, err = fmt.Fprintf(&data, "/busybox-lxd echo 0 > /proc/sys/net/ipv4/ip_forward\n")
	check(err)
	_, err = fmt.Fprintf(&data, "/busybox-lxd echo 0 > /proc/sys/net/ipv6/conf/all/forwarding\n")
	check(err)

	// configure networking
	_, err = fmt.Fprintf(&data, "/busybox-lxd udhcpc -R -b -i eth0 -s /lxd-udhcpc-default.script\n")
	check(err)

	for _, arg := range config.Entrypoint {
		_, err := fmt.Fprintf(&data, "\"%v\" ", shellEscape(arg))
		check(err)
	}
	for _, arg := range config.Cmd {
		_, err := fmt.Fprintf(&data, "\"%v\" ", shellEscape(arg))
		check(err)
	}

	_, err = fmt.Fprintf(&data, "&\n")
	check(err)

	// send SIGTERM to child if we receive SIGPWR
	// this makes `lxc stop` work
	_, err = fmt.Fprintf(&data, "trap 'kill -15 $child' PWR\n")
	check(err)
	_, err = fmt.Fprintf(&data, "child=$!; wait \"$child\"\n")
	check(err)

	header := &tar.Header{
		Name: "sbin/init",
		Mode: 0755,
		Size: int64(data.Len()),
	}

	err = writeTarFile(tarWriter, fileMap, header, &data)
	if err != nil {
		return fmt.Errorf("writing sbin/init: %w", err)
	}

	return nil
}

func writeMetadata(tarWriter *tar.Writer, name string, configFile *v1.ConfigFile) error {
	var metadata = lxdapi.ImageMetadata{
		Architecture: configFile.Architecture,
		CreationDate: time.Now().UTC().Unix(),
		Properties: map[string]string{
			"description": name,
		},
	}

	data, err := yaml.Marshal(&metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	header := &tar.Header{
		Name: "metadata.yaml",
		Mode: 0644,
		Size: int64(len(data)),
	}

	if err := tarWriter.WriteHeader(header); err != nil {
		return fmt.Errorf("failed to write tar header: %w", err)
	}

	if _, err := io.CopyN(tarWriter, bytes.NewReader(data), header.Size); err != nil {
		return fmt.Errorf("failed to write tar contents: %w", err)
	}

	return nil
}

func writeHostFile(tarWriter *tar.Writer, fileMap map[string]bool, dst string, src string, mode int64) error {
	fileinfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("can't stat `%s`: %w", src, err)
	}

	file, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("can't open `%s`: %w", src, err)
	}
	defer file.Close()

	header := &tar.Header{
		Name: dst,
		Mode: mode,
		Size: fileinfo.Size(),
	}

	err = writeTarFile(tarWriter, fileMap, header, file)
	if err != nil {
		return fmt.Errorf("writing sbin/init: %w", err)
	}

	return nil
}

func generateRootfsTar(img v1.Image, w io.Writer, name string) error {
	tarWriter := tar.NewWriter(w)
	defer tarWriter.Close()

	fileMap := map[string]bool{}

	configFile, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("retrieving image config file: %w", err)
	}
	config := &configFile.Config

	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("retrieving image layers: %w", err)
	}

	log.Debugf("write busybox")
	err = writeHostFile(tarWriter, fileMap, "/busybox-lxd", "/tmp/busybox", 0755)
	if err != nil {
		return fmt.Errorf("failed to write busybox: %w", err)
	}

	log.Debugf("write busybox-script")
	err = writeHostFile(tarWriter, fileMap, "/lxd-udhcpc-default.script", "/tmp/default.script", 0755)
	if err != nil {
		return fmt.Errorf("failed to write busybox-script: %w", err)
	}

	log.Debugf("write metadata")
	err = writeMetadata(tarWriter, name, configFile)
	if err != nil {
		return fmt.Errorf("failed to write /sbin/init: %w", err)
	}

	log.Debugf("write init")
	err = writeInit(tarWriter, fileMap, config)
	if err != nil {
		return fmt.Errorf("failed to write /sbin/init: %w", err)
	}

	// we iterate through the layers in reverse order because it makes handling
	// whiteout layers more efficient, since we can just keep track of the removed
	// files as we see .wh. layers and ignore those in previous layers.
	for i := len(layers) - 1; i >= 0; i-- {
		log.Debugf("write layer %v", i)

		layer := layers[i]
		layerReader, err := layer.Uncompressed()
		if err != nil {
			return fmt.Errorf("reading layer contents: %w", err)
		}
		defer layerReader.Close()
		tarReader := tar.NewReader(layerReader)
		for {
			header, err := tarReader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("reading tar: %w", err)
			}

			err = writeTarFile(tarWriter, fileMap, header, tarReader)
			if err != nil {
				return fmt.Errorf("writing tar: %w", err)
			}
		}
	}

	return nil
}

func getImageRemote(p layout.Path, ref name.Reference, platform v1.Platform) (v1.Image, error) {
	// the fetches platform may have addition info, so only compare the parts we care about
	matcher_platform := func(desc v1.Descriptor) bool {
		if desc.Platform == nil {
			return false
		}
		if desc.Platform.Architecture == platform.Architecture && desc.Platform.OS == platform.OS {
			return true
		}

		return false
	}
	matcher := func(desc v1.Descriptor) bool {
		return match.Name(ref.Name())(desc) && matcher_platform(desc)
	}

	log.Infof("fetch `%v` `%v` from remote", ref.Name(), platform.String())

	rmt, err := remote.Get(ref, remote.WithPlatform(platform))
	if err != nil {
		return nil, fmt.Errorf("failed to get remote: %w", err)
	}

	remote_img, err := rmt.Image()
	if err != nil {
		return nil, fmt.Errorf("failed to get remote image: %w", err)
	}

	opts := []layout.Option{}
	opts = append(opts, layout.WithAnnotations(map[string]string{
		imagespec.AnnotationRefName: ref.Name(),
	}))
	if err = p.ReplaceImage(remote_img, matcher, opts...); err != nil {
		return nil, fmt.Errorf("failed to replace OCI image: %w", err)
	}

	ii, err := p.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("failed to read image index: %w", err)
	}

	images, err := partial.FindImages(ii, matcher)
	if err != nil {
		return nil, fmt.Errorf("failed to search through image index: %w", err)
	}
	if len(images) < 1 {
		return nil, fmt.Errorf("BUG: can't find the image we just pulled: `%v` `%v`", ref.Name(), platform.String())
	}

	return images[0], nil
}

func currentPlatform() v1.Platform {
	return v1.Platform{
		Architecture: runtime.GOARCH,
		OS:           runtime.GOOS,
	}
}

type ImageSpec struct {
	Image string
}

func getImage(ociDir string, spec ImageSpec) (v1.Image, error) {
	platform := currentPlatform()

	ref, err := name.ParseReference(spec.Image)
	if err != nil {
		return nil, fmt.Errorf("parsing reference %q: %w", spec.Image, err)
	}

	p, err := layout.FromPath(ociDir)
	if err != nil {
		p, err = layout.Write(ociDir, empty.Index)
		if err != nil {
			return nil, fmt.Errorf("failed to create empty layout: %w", err)
		}
	}

	img, err := getImageRemote(p, ref, platform)
	if err != nil {
		return nil, fmt.Errorf("failed to get image: %w", err)
	}

	return img, nil
}

func generateRootfs(dst io.Writer, img v1.Image, name string) error {
	gzipWriter := gzip.NewWriter(dst)
	defer gzipWriter.Close()

	err := generateRootfsTar(img, gzipWriter, name)
	if err != nil {
		return fmt.Errorf("failed to generate rootfs: %w", err)
	}

	return nil
}

func hashToV1(hash []byte) v1.Hash {
	return v1.Hash{
		Algorithm: "sha256",
		Hex:       hex.EncodeToString(hash),
	}
}

func hashToV1Sized(hash [32]byte) v1.Hash {
	return hashToV1(hash[:])
}

func hashFile(path string) (*v1.Hash, error) {
	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open `%s` for hashing: %w", path, err)
	}
	defer input.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, input); err != nil {
		return nil, fmt.Errorf("failed to read `%s` for hashing: %w", path, err)
	}

	v1hash := hashToV1(hash.Sum(nil))
	return &v1hash, nil
}

func removeUnusedLxdMetadata(usedLxdMetadata map[string]bool, imageDir string) error {
	files, err := ioutil.ReadDir(imageDir)
	if err != nil {
		return fmt.Errorf("failed to read images dir: %w", err)
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		name := filepath.Base(file.Name())
		ext := filepath.Ext(file.Name())

		if ext != ".yaml" {
			continue
		}

		if _, ok := usedLxdMetadata[name]; ok {
			continue
		}

		log.Debugf("delete unused metadata `%s`", name)
		err = os.Remove(filepath.Join(imageDir, file.Name()))
		if err != nil {
			return fmt.Errorf("failed to delete unused metadata `%s`: %w", name, err)
		}
	}

	return nil
}

func removeUnusedLxdImages(usedLxdImages map[string]bool, imageDir string) error {
	files, err := ioutil.ReadDir(imageDir)
	if err != nil {
		return fmt.Errorf("failed to read images dir: %w", err)
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		name := filepath.Base(file.Name())
		ext := filepath.Ext(file.Name())

		if ext != ".rootfs" {
			continue
		}

		if _, ok := usedLxdImages[name]; ok {
			continue
		}

		log.Debugf("delete unused rootfs `%s`", name)
		err = os.Remove(filepath.Join(imageDir, file.Name()))
		if err != nil {
			return fmt.Errorf("failed to delete unused rootfs `%s`: %w", name, err)
		}
	}

	return nil
}

func removeUnusedOciImages(usedOciImages map[v1.Hash]bool, ociDir string) error {
	p, err := layout.FromPath(ociDir)
	if err != nil {
		return fmt.Errorf("failed to open layout dir: %w", err)
	}

	matcher := func(descriptor v1.Descriptor) bool {
		if !descriptor.MediaType.IsImage() {
			return false
		}

		if _, ok := usedOciImages[descriptor.Digest]; ok {
			return false
		}

		log.Debugf("delete unused OCI image from index: %+v", descriptor)

		return true
	}

	err = p.RemoveDescriptors(matcher)
	if err != nil {
		return fmt.Errorf("failed to delete unused OCI images: %w", err)
	}

	return nil
}

func updateAll(ociDir string, specDir string, imageDir string) error {
	var usedOciImages = map[v1.Hash]bool{}
	var usedLxdImages = map[string]bool{}
	var usedLxdMetadata = map[string]bool{}

	files, err := ioutil.ReadDir(specDir)
	if err != nil {
		return fmt.Errorf("failed to open imagespec dir `%s`: %w", specDir, err)
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		ext := filepath.Ext(file.Name())
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		name := strings.TrimSuffix(filepath.Base(file.Name()), ext)
		metadataFilename := fmt.Sprintf("%s.meta", name)
		metadataFilepath := filepath.Join(imageDir, metadataFilename)

		usedLxdMetadata[name] = true

		// XXX: we read it into RAM instead of opening a reader so we can be sure
		//      the hash is of the data we parsed when somebody writes to the file
		//      while we're reading
		specBytes, err := ioutil.ReadFile(filepath.Join(specDir, file.Name()))
		if err != nil {
			log.Errorf("failed to read spec file `%v`: %w", name, err)
			continue
		}
		specHash := hashToV1Sized(sha256.Sum256(specBytes))

		// parse image spec
		spec := ImageSpec{}

		decoder := yaml.NewDecoder(bytes.NewReader(specBytes))
		decoder.KnownFields(true)

		err = decoder.Decode(&spec)
		if err != nil {
			log.Errorf("failed to parse `%v`: %w", name, err)
			continue
		}

		img, err := getImage(ociDir, spec)
		if err != nil {
			log.Errorf("failed to update `%v`: %w", name, err)
			continue
		}

		ociHash, err := img.Digest()
		if err != nil {
			log.Errorf("failed to hash oci for `%v`: %w", name, err)
			continue
		}

		oldRootMeta, err := common.ReadRootfsMetaData(metadataFilepath)
		if err == nil {
			// we already have an LXD image, check if we need to update

			if oldRootMeta.SpecDigest == specHash && oldRootMeta.OciImageDigest == ociHash {
				log.Infof("`%v` didn't change, skip", name)

				usedOciImages[ociHash] = true
				usedLxdImages[oldRootMeta.Filename] = true

				continue
			}
		} else {
			log.Debugf("old metadata not read: %w", err)
		}

		// write rootfs
		file, err := os.CreateTemp(imageDir, name)
		if err != nil {
			log.Errorf("failed to open `%s`: %w", name, err)
			continue
		}

		log.Infof("generate rootfs at %v", file.Name())
		err = generateRootfs(file, img, name)
		file.Close()
		if err != nil {
			log.Errorf("failed to generate rootfs for `%v`: %w", name, err)
			continue
		}

		rootfsHash, err := hashFile(file.Name())
		if err != nil {
			log.Errorf("failed to hash rootfs for `%v`: %w", name, err)
			continue
		}

		// XXX: the rootfs might already exist in case the metadata
		//      changed but the result didn't. So do an atomic rename
		rootfsFilename := fmt.Sprintf("%s-%v.rootfs", name, rootfsHash.Hex)
		err = os.Rename(file.Name(), filepath.Join(imageDir, rootfsFilename))
		if err != nil {
			log.Errorf("failed to rename rootfs for `%v`: %w", name, err)
			continue
		}

		// write metadata
		rootMeta := common.RootfsMetadata{
			SpecDigest:     specHash,
			OciImageDigest: ociHash,
			LxdImageDigest: *rootfsHash,
			Filename:       rootfsFilename,
		}

		file, err = os.CreateTemp(imageDir, metadataFilename)
		if err != nil {
			log.Errorf("failed to open temp metadata file `%v`: %w", name, err)
			continue
		}

		err = yaml.NewEncoder(file).Encode(&rootMeta)
		file.Close()
		if err != nil {
			log.Errorf("failed to encode metadata for `%v`: %w", name, err)
			continue
		}

		// atomically replace old metadata
		err = os.Rename(file.Name(), metadataFilepath)
		if err != nil {
			log.Errorf("failed to rename metadata for `%v`: %w", name, err)
			continue
		}

		usedOciImages[ociHash] = true
		usedLxdImages[rootfsFilename] = true
	}

	log.Infof("delete unused LXD metadata")
	err = removeUnusedLxdMetadata(usedLxdMetadata, imageDir)
	if err != nil {
		return fmt.Errorf("failed to delete unused LXD metadata: %w", err)
	}

	log.Infof("delete unused LXD images")
	err = removeUnusedLxdImages(usedLxdImages, imageDir)
	if err != nil {
		return fmt.Errorf("failed to delete unused LXD images: %w", err)
	}

	log.Infof("delete unused OCI images")
	err = removeUnusedOciImages(usedOciImages, ociDir)
	if err != nil {
		return fmt.Errorf("failed to delete unused OCI images: %w", err)
	}

	return nil
}

func listUsedBlobs(ociDir string) (map[v1.Hash]bool, error) {
	var blobs = map[v1.Hash]bool{}

	p, err := layout.FromPath(ociDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open layout dir %w", err)
	}

	ii, err := p.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("failed to read image index: %w", err)
	}

	indexManifest, err := ii.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("failed to open index-manifest: %w", err)
	}

	for _, descriptor := range indexManifest.Manifests {
		blobs[descriptor.Digest] = true

		// if it is not an image, ignore it
		if !descriptor.MediaType.IsImage() {
			continue
		}

		img, err := ii.Image(descriptor.Digest)
		if err != nil {
			return nil, fmt.Errorf("failed to open image `%v`: %w", descriptor.Digest, err)
		}

		configFile, err := img.ConfigFile()
		if err != nil {
			return nil, fmt.Errorf("failed to get image configfile %w", err)
		}

		for _, hash := range configFile.RootFS.DiffIDs {
			blobs[hash] = true
		}

		imageManifest, err := img.Manifest()
		if err != nil {
			return nil, fmt.Errorf("failed get image manifest: %w", err)
		}
		blobs[imageManifest.Config.Digest] = true

		for _, descriptor := range imageManifest.Layers {
			blobs[descriptor.Digest] = true
		}
	}

	return blobs, nil
}

func deleteUnusedBlobs(ociDir string, usedBlobs map[v1.Hash]bool) error {
	blobsDir := filepath.Join(ociDir, "blobs")

	files, err := ioutil.ReadDir(blobsDir)
	if err != nil {
		return fmt.Errorf("failed to read blobs dir: %w", err)
	}

	for _, file := range files {
		if !file.IsDir() {
			continue
		}

		algorithm := file.Name()

		algorithmDir := filepath.Join(blobsDir, algorithm)

		files, err := ioutil.ReadDir(algorithmDir)
		if err != nil {
			return fmt.Errorf("failed to read algorithm dir: %w", err)
		}

		for _, file := range files {
			if file.IsDir() {
				continue
			}

			hash := v1.Hash{
				Algorithm: algorithm,
				Hex:       file.Name(),
			}

			if _, ok := usedBlobs[hash]; ok {
				continue
			}

			log.Infof("delete unused blob `%v`", hash)

			err := os.Remove(filepath.Join(algorithmDir, file.Name()))
			if err != nil {
				return fmt.Errorf("failed to delete unused blob `%v`: %w", hash, err)
			}
		}
	}

	return nil
}

func main() {
	log = common.MakeLogger()

	var ociDir string
	var imageDir string
	var specDir string

	var rootCmd = &cobra.Command{
		Use:   "lxdocker",
		Short: "generate LXD images from docker containers",
		Run: func(cmd *cobra.Command, args []string) {
			err := os.MkdirAll(imageDir, os.ModePerm)
			if err != nil {
				log.Fatalf("failed to create directory `%s`: %w", imageDir, err)
				return
			}

			log.Infof("update all images")
			err = updateAll(ociDir, specDir, imageDir)
			if err != nil {
				log.Fatalf("failed to update all images: %w", err)
				return
			}

			log.Infof("look for and delete unused blobs")
			usedBlobs, err := listUsedBlobs(ociDir)
			if err != nil {
				log.Fatalf("failed to list used blobs: %w", err)
				return
			}

			err = deleteUnusedBlobs(ociDir, usedBlobs)
			if err != nil {
				log.Fatalf("failed to delete unused blobs: %w", err)
				return
			}

			log.Infof("Done")
		},
	}

	rootCmd.Flags().StringVar(&ociDir, "cache", "", "path to OCI cache")
	rootCmd.Flags().StringVar(&imageDir, "lxdimages", "", "path to directory for generated LXD images")
	rootCmd.Flags().StringVar(&specDir, "specs", "", "path to directory with LXD image specifications")

	rootCmd.MarkFlagRequired("cache")
	rootCmd.MarkFlagRequired("lxdimages")
	rootCmd.MarkFlagRequired("specs")

	rootCmd.Execute()
}
