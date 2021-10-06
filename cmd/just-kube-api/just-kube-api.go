package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"path"
	"runtime"
	"strings"

	"github.com/kyoh86/xdg"
	"github.com/okzk/sdnotify"
	log "github.com/sirupsen/logrus"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func main() {
	assetsDir := path.Join(xdg.DataDirs()[0], "just-kube-api")
	apiServerVersion := "v1.22.2"
	etcdVersion := "v3.5.0"
	kubeconfigFile := "kubeconfig"
	bootstrap := false

	flag.StringVar(&assetsDir, "assets-directory", assetsDir, "directory for etcd and kube-apiserver binaries")
	flag.StringVar(&kubeconfigFile, "kubeconfig-out", kubeconfigFile, "path of kubeconfig written once the cluster is ready")
	flag.StringVar(&apiServerVersion, "apiserver-version", apiServerVersion, "kube-apiserver version to use")
	flag.StringVar(&etcdVersion, "etcd", etcdVersion, "etcd version to use")
	flag.BoolVar(&bootstrap, "bootstrap", bootstrap, "just download binaries and exit")

	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	err := os.MkdirAll(assetsDir, os.FileMode(0755))
	if err != nil {
		log.WithError(err).Fatal("failed to create assets dir")
	}

	err = ensureKubeApiserver(ctx, assetsDir, apiServerVersion)
	if err != nil {
		log.WithError(err).Fatal("failed to set up kube-apiserver")
	}

	err = ensureEtcd(ctx, assetsDir, etcdVersion)
	if err != nil {
		log.WithError(err).Fatal("failed to set up etcd")
	}

	if bootstrap {
		os.Exit(0)
	}

	env := &envtest.Environment{
		BinaryAssetsDirectory: assetsDir,
	}
	_, err = env.Start()
	if err != nil {
		log.WithError(err).Fatal("start")
	}
	defer func() {
		err := env.Stop()
		if err != nil {
			log.WithError(err).Warn("failed to stop env")
		}
	}()

	u, err := env.AddUser(envtest.User{Name: "admin", Groups: []string{"system:masters"}}, nil)
	if err != nil {
		log.WithError(err).Fatal("failed to add user")
	}

	encoded, err := u.KubeConfig()
	if err != nil {
		log.WithError(err).Fatal("failed to get kubeconfig")
	}

	userHome, _ := os.UserHomeDir()
	kubeconfPath := path.Join(userHome, ".kube")
	err = os.MkdirAll(kubeconfPath, os.FileMode(0755))
	if err != nil {
		log.WithError(err).Fatal("failed to write kubeconfig directory")
	}
	kubeconfigFilePath := path.Join(kubeconfPath, kubeconfigFile)
	err = ioutil.WriteFile(kubeconfigFilePath, encoded, os.FileMode(0644))
	if err != nil {
		log.WithError(err).Fatal("failed to write kubeconfig")
	}

	fmt.Printf("API ready! KubeConfig written to '%s'\n", kubeconfigFilePath)

	sdnotify.Ready()
	<-ctx.Done()
}

func ensureKubeApiserver(ctx context.Context, destinationDir string, version string) error {
	name := fmt.Sprintf("kube-apiserver-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
	dest := path.Join(destinationDir, name)

	fileUrl := fmt.Sprintf("https://dl.k8s.io/%s/bin/%s/%s/kube-apiserver", version, runtime.GOOS, runtime.GOARCH)
	hashUrl := fileUrl + ".sha256"

	hashReq, err := http.NewRequestWithContext(ctx, http.MethodGet, hashUrl, nil)
	if err != nil {
		return err
	}

	hashResp, err := http.DefaultClient.Do(hashReq)
	if err != nil {
		return err
	}
	defer hashResp.Body.Close()

	if hashResp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET '%s' status not ok: %s", hashResp.Request.URL, hashResp.Status)
	}

	rawHashResp, err := io.ReadAll(hashResp.Body)
	if err != nil {
		return err
	}

	expected, err := hex.DecodeString(strings.TrimSpace(string(rawHashResp)))
	if err != nil {
		return err
	}

	fileChecker := sha256.New()
	file, _ := os.Open(dest)
	if file != nil {
		defer file.Close()
		_, err := io.Copy(fileChecker, file)
		if err != nil {
			return err
		}
	}

	if !bytes.Equal(fileChecker.Sum(nil), expected) {
		log.WithField("url", fileUrl).Info("Starting download of kube-apiserver")

		fileReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fileUrl, nil)
		if err != nil {
			return err
		}

		fileResp, err := http.DefaultClient.Do(fileReq)
		if err != nil {
			return err
		}
		defer fileResp.Body.Close()

		if fileResp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET '%s' status not ok: %s", fileResp.Request.URL, fileResp.Status)
		}

		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(0755))
		if err != nil {
			return err
		}

		fileChecker.Reset()
		destWriter := io.MultiWriter(f, fileChecker)

		_, err = io.Copy(destWriter, fileResp.Body)
		if err != nil {
			return err
		}

		if !bytes.Equal(fileChecker.Sum(nil), expected) {
			_ = os.Remove(f.Name())
			return fmt.Errorf("hash mismatch in file loaded from '%s'", fileResp.Request.URL)
		}

		log.WithField("url", fileUrl).Info("download of kube-apiserver complete")
	}

	link := path.Join(destinationDir, "kube-apiserver")

	_ = os.Remove(link)
	return os.Symlink(dest, link)
}

func ensureEtcd(ctx context.Context, destinationDir string, version string) error {
	name := fmt.Sprintf("etcd-%s-%s-%s", version, runtime.GOOS, runtime.GOARCH)
	dest := path.Join(destinationDir, name)

	fileUrl := fmt.Sprintf("https://github.com/etcd-io/etcd/releases/download/%s/%s.tar.gz", version, name)
	hashUrl := fmt.Sprintf("https://github.com/etcd-io/etcd/releases/download/%s/SHA256SUMS", version)

	hashReq, err := http.NewRequestWithContext(ctx, http.MethodGet, hashUrl, nil)
	if err != nil {
		return err
	}

	hashResp, err := http.DefaultClient.Do(hashReq)
	if err != nil {
		return err
	}
	defer hashResp.Body.Close()

	if hashResp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET '%s' status not ok: %s", hashResp.Request.URL, hashResp.Status)
	}

	rawHashResp, err := io.ReadAll(hashResp.Body)
	if err != nil {
		return err
	}

	var expected []byte

	for _, line := range strings.Split(string(rawHashResp), "\n") {
		if strings.HasSuffix(line, name+".tar.gz") {
			parts := strings.Split(line, " ")
			expected, err = hex.DecodeString(parts[0])
			if err != nil {
				return err
			}
			break
		}
	}

	if len(expected) == 0 {
		return fmt.Errorf("shasums does not contain expected entry for archive %s", name)
	}

	fileChecker := sha256.New()
	file, _ := os.Open(dest)
	if file != nil {
		defer file.Close()
		_, err := io.Copy(fileChecker, file)
		if err != nil {
			return err
		}
	}

	if !bytes.Equal(fileChecker.Sum(nil), expected) {
		log.WithField("url", fileUrl).Info("Starting download of etcd")

		fileReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fileUrl, nil)
		if err != nil {
			return err
		}

		fileResp, err := http.DefaultClient.Do(fileReq)
		if err != nil {
			return err
		}
		defer fileResp.Body.Close()

		if fileResp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET '%s' status not ok: %s", fileResp.Request.URL, fileResp.Status)
		}

		f, err := os.Create(dest)
		if err != nil {
			return err
		}
		defer f.Close()

		fileChecker.Reset()
		destWriter := io.MultiWriter(f, fileChecker)

		_, err = io.Copy(destWriter, fileResp.Body)
		if err != nil {
			return err
		}

		if !bytes.Equal(fileChecker.Sum(nil), expected) {
			_ = os.Remove(f.Name())
			return fmt.Errorf("hash mismatch in file loaded from '%s'", fileResp.Request.URL)
		}

		log.WithField("url", fileUrl).Info("download of etcd complete")
	}

	archive, err := os.Open(dest)
	if err != nil {
		return err
	}
	defer archive.Close()

	compressedReader, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}

	archiveReader := tar.NewReader(compressedReader)

	for {
		header, err := archiveReader.Next()
		if err != nil {
			return err
		}

		if header.Name == name+"/etcd" {
			f, err := os.OpenFile(path.Join(destinationDir, "etcd"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(0755))
			if err != nil {
				return err
			}
			defer f.Close()

			_, err = io.CopyN(f, archiveReader, header.Size)
			if err != nil {
				_ = os.Remove(f.Name())
				return err
			}

			return nil
		}
	}
}
