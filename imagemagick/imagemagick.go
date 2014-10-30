package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"

	"runtime"
	"strings"

	"code.google.com/p/go.net/context"
	auth "code.google.com/p/google-api-go-client/oauth2/v2"

	"github.com/gographics/imagick/imagick"
	"github.com/golang/oauth2/google"
	"google.golang.org/cloud"
	"google.golang.org/cloud/storage"
)

const (
	projectID     = "abelana-222"
	outputBucket  = "abelana"
	listenAddress = "0.0.0.0:8080"
	pushURL       = "https://endpoints-dot-abelana-222.appspot.com/photopush/"
	authEmail     = "abelana-222@appspot.gserviceaccount.com"
)

// map with the suffixes and sizes to generate
var sizes = map[string]struct{ x, y uint }{
	"a": {480, 800},
	"b": {768, 768},
	"c": {1080, 1080},
	"d": {1440, 1440},
	"e": {1200, 1200},
	"f": {1536, 1536},
	"g": {720, 720},
	"h": {640, 640},
	"i": {750, 750},
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	flag.Parse()

	transport := google.NewComputeEngineConfig("").NewTransport()
	ctx := cloud.NewContext(projectID, &http.Client{Transport: transport})

	err := http.ListenAndServe(listenAddress, &server{ctx})
	if err != nil {
		log.Fatal(err)
	}
}

type server struct{ ctx context.Context }

func (s server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthcheck":
		return
	case "/notice/abelana-in":
	default:
		http.NotFound(w, r)
		return
	}

	bucket, name := r.PostFormValue("bucket"), r.PostFormValue("name")
	if bucket == "" || name == "" {
		http.Error(w, "missing bucket or name", http.StatusBadRequest)
		return
	}

	token := r.Header.Get("Authorization")
	if ok, err := authorized(token); !ok {
		if err != nil {
			log.Printf("authorize: %v", err)
		}
		http.Error(w, "you're not authorized", http.StatusForbidden)
		return
	}

	if err := s.processImage(bucket, name); err != nil {
		// TODO: should this remove uploaded images?
		log.Println(err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := notifyDone(name, token); err != nil {
		log.Println(err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s server) processImage(bucket, name string) error {
	r, err := storage.NewReader(s.ctx, bucket, name)
	if err != nil {
		return fmt.Errorf("storage reader: %v", err)
	}
	img, err := ioutil.ReadAll(r)
	r.Close()
	if err != nil {
		return fmt.Errorf("read image: %v", err)
	}

	wand := imagick.NewMagickWand()
	wand.ReadImageBlob(img)
	if err := wand.SetImageFormat("WEBP"); err != nil {
		return fmt.Errorf("set WEBP format: %v", err)
	}

	errc := make(chan error, len(sizes))
	for suffix, size := range sizes {
		go func(wand *imagick.Wand, suffix, size string) {
			errc <- func() error {
				if err := wand.AdaptiveResizeImage(size.x, size.y); err != nil {
					return fmt.Errorf("resize: %v", err)
				}

				target := name
				if sep := strings.LastIndex(target, "."); sep >= 0 {
					target = target[:sep]
				}
				target = fmt.Sprintf("%s_%s.webp", target, suffix)

				w := storage.NewWriter(s.ctx, outputBucket, target, nil)
				if _, err := w.Write(wand.GetImageBlob()); err != nil {
					return fmt.Errorf("new writer: %v", err)
				}
				if err := w.Close(); err != nil {
					return fmt.Errorf("close object writer: %v", err)
				}
				if _, err := w.Object(); err != nil {
					return fmt.Errorf("write image: %v", err)
				}
				return nil
			}()
		}(wand.Clone(), suffix, size)
	}

	for _ = range sizes {
		if err := <-errc; err != nil {
			return err
		}
	}
	return nil
}

func authorized(token string) (ok bool, err error) {
	if fs := strings.Fields(token); len(fs) == 2 && fs[0] == "Bearer" {
		token = fs[1]
	} else {
		return false, nil
	}

	svc, err := auth.New(http.DefaultClient)
	if err != nil {
		return false, err
	}
	tok, err := svc.Tokeninfo().Access_token(token).Do()
	if err != nil {
		return false, err
	}

	return tok.Email == authEmail, nil
}

func notifyDone(name, token string) (err error) {
	req, err := http.NewRequest("POST", pushURL+name, &bytes.Buffer{})
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", token)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("photo push: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("photo push status: %v", res.Status)
	}
	return nil
}
