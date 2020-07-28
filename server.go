package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/f9a/exit"
	"github.com/f9a/gov"
	"github.com/f9a/gov/service"
	"github.com/f9a/netl"
	oz "github.com/go-ozzo/ozzo-validation"
	"go.uber.org/zap"
)

var (
	MetadataName    = "catd"
	MetadataVersion = "none"
	MetadataCommit  = "none"
	MetadataMTime   = "0001-01-01T00:00:00Z"
)

type flags struct {
	IsMetadata  bool
	File        string
	IsRandomKey bool
	Timeout     time.Duration
}

func (f flags) Validate() error {
	if !f.IsMetadata {
		return oz.ValidateStruct(&f,
			oz.Field(&f.File, oz.Required, oz.Length(1, 1_000_000)),
		)
	}

	return nil
}

type singleFileServer struct {
	Key    string
	File   string
	Logger *zap.Logger
}

func (s singleFileServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Logger.Info("new request", zap.String("method", r.Method), zap.Stringer("url", r.URL))

	if s.Key != "" {
		if r.URL.Path != "/"+s.Key {
			status := http.StatusNotFound
			http.Error(w, http.StatusText(status), status)
			return
		}
	}

	stat, err := os.Stat(s.File)
	if err != nil {
		http.Error(w, "couldn't read states from file", http.StatusInternalServerError)
		return
	}

	fh, err := os.Open(s.File)
	if err != nil {
		http.Error(w, "couldn't open file", http.StatusInternalServerError)
		return
	}
	defer func() {
		fh.Close()
	}()

	http.ServeContent(w, r, s.File, stat.ModTime(), fh)
}

const (
	letterBytes   = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	letterIdxBits = 6
	letterIdxMask = 1<<letterIdxBits - 1
)

func randStr(length int) (string, error) {
	result := make([]byte, length)
	bufferSize := int(float64(length) * 1.3)
	for i, j, randomBytes := 0, 0, []byte{}; i < length; j++ {
		if j%bufferSize == 0 {
			randomBytes = make([]byte, bufferSize)
			_, err := rand.Read(randomBytes)
			if err != nil {
				return "", fmt.Errorf("couldn't create random bytes: %v", err)
			}
		}
		if idx := int(randomBytes[j%length] & letterIdxMask); idx < len(letterBytes) {
			result[i] = letterBytes[idx]
			i++
		}
	}

	return string(result), nil
}

func main() {
	defer exit.Catch()

	flags := flags{}
	flagSet := flag.NewFlagSet("catd", flag.ExitOnError)
	flagSet.BoolVar(&flags.IsMetadata, "version", false, "Show metadata")
	flagSet.BoolVar(&flags.IsMetadata, "metadata", false, "Show metadata")
	flagSet.StringVar(&flags.File, "file", "", "Path to file (required)")
	flagSet.BoolVar(&flags.IsRandomKey, "random-key", false, "File is available under random-key")
	flagSet.DurationVar(&flags.Timeout, "timeout", 0, "Shutdown server after duration")

	netCfg := netl.Config{}
	netl.FlagSet(flagSet, &netCfg)

	err := flagSet.Parse(os.Args[1:])
	if err != nil {
		fmt.Printf("wrong arguments: %v\n", err)
		flagSet.Usage()
		exit.With(1)
	}

	err = flags.Validate()
	if err != nil {
		fmt.Printf("wrong arguments: %v\n", err)
		flagSet.Usage()
		exit.With(1)
	}

	if flags.IsMetadata {
		fmt.Printf("Name: %s\nVersion: %s\nCommit: %s\nMTime: %s\n", MetadataName, MetadataVersion, MetadataCommit, MetadataMTime)
		exit.With(0)
	}

	logger, err := zap.NewProduction()
	exit.OnErrf(err, "couldn't create new production logger")

	singleFileServer := singleFileServer{
		File:   flags.File,
		Logger: logger.Named("single-file-server"),
	}

	if flags.IsRandomKey {
		key, err := randStr(8)
		exit.OnErr(err)
		singleFileServer.Key = key
		fmt.Println("RANDOM-KEY:", key)
	}

	tcpListener, err := netCfg.Listen()
	exit.OnErrf(err, "couldn't create tcp-listener")

	httpServer := &http.Server{
		Handler: singleFileServer,
	}

	stop := make(chan struct{})
	go func() {
		osStopSignal := gov.OSStopSignal()

		if flags.Timeout != 0 {
			select {
			case <-time.After(flags.Timeout):
				close(stop)
			case <-osStopSignal:
				close(stop)
			}

			return
		}

		<-osStopSignal
		close(stop)
	}()

	sm := gov.New(gov.StopOnSignal(stop))
	sm.Add(service.NewHTTPListener(httpServer, tcpListener))
	err = sm.Start()
	exit.OnErrf(err, "error while starting services")
}
