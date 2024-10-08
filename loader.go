package conflate

import (
	ctx "context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	pkgurl "net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"cloud.google.com/go/storage"
)

const windowsOS = "windows"

var (
	goos        = runtime.GOOS
	emptyURL    = pkgurl.URL{}
	getwd       = os.Getwd
	driveLetter = regexp.MustCompile(`^[A-Za-z]:.*$`)

	errBlankFilePath = errors.New("the file path is blank")
	errFailedToLoad  = errors.New("failed to load url")
	errRecursiveURL  = errors.New("the url recursively includes itself")
)

type loader struct {
	newFiledata func([]byte, *pkgurl.URL) (filedata, error)
}

func (l *loader) loadURLsRecursive(parentUrls []*pkgurl.URL, urls ...*pkgurl.URL) (filedatas, error) {
	var allData filedatas

	for _, url := range urls {
		data, err := l.loadURLRecursive(parentUrls, url)
		if err != nil {
			return nil, err
		}

		allData = append(allData, data...)
	}

	return allData, nil
}

func (l *loader) loadURLRecursive(parentUrls []*pkgurl.URL, url *pkgurl.URL) (filedatas, error) {
	data, err := loadURL(url)
	if err != nil {
		return nil, err
	}

	fdata, err := l.newFiledata(data, url)
	if err != nil {
		return nil, err
	}

	return l.loadDatumRecursive(parentUrls, url, &fdata)
}

func (l *loader) loadDataRecursive(parentUrls []*pkgurl.URL, data ...filedata) (filedatas, error) {
	var allData filedatas

	for _, datum := range data {
		datum := datum

		childData, err := l.loadDatumRecursive(parentUrls, nil, &datum)
		if err != nil {
			return nil, err
		}

		allData = append(allData, childData...)
	}

	return allData, nil
}

func (l *loader) loadDatumRecursive(parentUrls []*pkgurl.URL, url *pkgurl.URL, data *filedata) (filedatas, error) {
	if data.isEmpty() {
		return nil, nil
	}

	if containsURL(url, parentUrls) {
		return nil, fmt.Errorf("%w (%v)", errRecursiveURL, url)
	}

	childUrls, err := toURLs(url, data.includes...)
	if err != nil {
		return nil, err
	}

	var newParentUrls []*pkgurl.URL

	newParentUrls = append(newParentUrls, parentUrls...)

	if url != nil {
		newParentUrls = append(newParentUrls, url)
	}

	childData, err := l.loadURLsRecursive(newParentUrls, childUrls...)
	if err != nil {
		return nil, err
	}

	var allData filedatas

	allData = append(allData, childData...)
	allData = append(allData, *data)

	return allData, nil
}

func (l *loader) wrapFiledata(bytes []byte) (filedata, error) {
	return l.newFiledata(bytes, &emptyURL)
}

func (l *loader) wrapFiledatas(bytes ...[]byte) (filedatas, error) {
	var fds []filedata

	for _, b := range bytes {
		fd, err := l.wrapFiledata(b)
		if err != nil {
			return nil, err
		}

		fds = append(fds, fd)
	}

	return fds, nil
}

func loadURL(url *pkgurl.URL) ([]byte, error) {
	if url.Scheme == "file" {
		// attempt to load locally handling case where we are loading from fifo etc
		b, err := ioutil.ReadFile(getPath(url.Path))
		if err == nil {
			return b, nil
		}
	}

	if url.Scheme == "gs" {
		return loadConfigFromBucket(url)
	}

	client := http.Client{Transport: newTransport()}

	resp, err := client.Get(url.String())
	if err != nil {
		return nil, err
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("error when closing response body: %v", err.Error())
		}
	}()

	data, err := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w : %v : %v", errFailedToLoad, resp.StatusCode, url.String())
	}

	return data, err
}

func loadConfigFromBucket(url *pkgurl.URL) ([]byte, error) {
	bucket := url.Host
	fileName := strings.TrimLeft(url.Path, "/")

	context := ctx.Background()

	client, err := storage.NewClient(context)
	if err != nil {
		return nil, fmt.Errorf("unable to create gcp storage client: %w", err)
	}

	bucketHandler := client.Bucket(bucket)

	rc, err := bucketHandler.Object(fileName).NewReader(context)
	if err != nil {
		return nil, fmt.Errorf("unable to open file from bucket %q, file %q: %w", bucket, fileName, err)
	}

	defer func() {
		if err := rc.Close(); err != nil {
			log.Printf("error when closing the bucket handler reader: %v", err.Error())
		}
	}()

	slurp, err := ioutil.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("unable to read data from bucket %q, file %q: %w", bucket, fileName, err)
	}

	return slurp, nil
}

func newTransport() *http.Transport {
	const (
		conns            = 100
		timeout          = 30
		handshakeTimeout = 10
		idleConnTimeout  = 90
	)

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   timeout * time.Second,
			KeepAlive: timeout * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          conns,
		IdleConnTimeout:       idleConnTimeout * time.Second,
		TLSHandshakeTimeout:   handshakeTimeout * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	transport.RegisterProtocol("file", http.NewFileTransport(http.Dir("/"))) //nolint:gosec // safe in this case

	return transport
}

func toURLs(rootURL *pkgurl.URL, paths ...string) ([]*pkgurl.URL, error) {
	var urls []*pkgurl.URL

	for _, path := range paths {
		url, err := toURL(rootURL, path)
		if err != nil {
			return nil, err
		}

		urls = append(urls, url)
	}

	return urls, nil
}

func toURL(rootURL *pkgurl.URL, path string) (*pkgurl.URL, error) {
	if path == "" {
		return &emptyURL, errBlankFilePath
	}

	var err error

	if rootURL == nil {
		rootURL, err = workingDir()
		if err != nil {
			return &emptyURL, err
		}
	}

	url, err := pkgurl.Parse(setPath(path))
	if err != nil {
		return &emptyURL, fmt.Errorf("could not parse path: %w", err)
	}

	if !url.IsAbs() {
		url = rootURL.ResolveReference(url)
		url.RawQuery = rootURL.RawQuery
	}

	return url, nil
}

func containsURL(searchURL *pkgurl.URL, urls []*pkgurl.URL) bool {
	if searchURL == nil {
		return false
	}

	for _, u := range urls {
		if *u == *searchURL {
			return true
		}
	}

	return false
}

func workingDir() (*pkgurl.URL, error) {
	rootPath, err := getwd()
	if err != nil {
		return nil, err
	}

	rootURL, err := pkgurl.Parse("file://" + setPath(rootPath) + "/")
	if err != nil {
		return nil, err
	}

	return rootURL, nil
}

func setPath(path string) string {
	if goos == windowsOS {
		// https://blogs.msdn.microsoft.com/ie/2006/12/06/file-uris-in-windows/
		path = strings.Replace(path, `\`, `/`, -1)
		path = strings.TrimLeft(path, `/`)

		if driveLetter.MatchString(path) {
			path = `/` + path
		}
	}

	return path
}

func getPath(path string) string {
	if goos == windowsOS {
		// https://blogs.msdn.microsoft.com/ie/2006/12/06/file-uris-in-windows/
		path = strings.TrimLeft(path, `/`)

		if !driveLetter.MatchString(path) {
			path = `//` + path
		}

		path = strings.Replace(path, `/`, `\`, -1)
	}

	return path
}
