package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abtiwary/go-bom-radar-gif/bom-radar-gif-encoder"

	log "github.com/sirupsen/logrus"
)

func initLogger() {
	log.SetFormatter(&log.JSONFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.InfoLevel)
}

func getBomGif(w http.ResponseWriter, r *http.Request) {
	bomEncoder, err := bom_radar_gif_encoder.NewBomRadarGifEncoder(
			"IDR713",
			"IDR71B",
			"/home/pimeson/temp/",
			)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}

	defer bomEncoder.Close()

	gifBytes, err := bomEncoder.MakeGif()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}

	w.Header().Set("Content-Type", "image/gif")
	w.Write(gifBytes)
}

func main() {
	initLogger()

	httpServer := &http.Server{
		Addr: ":9099",
	}

	http.HandleFunc("/", getBomGif)

	go func() {
		err := http.ListenAndServe(":9099", nil)
		if errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server closed")
		} else if err != nil {
			log.WithError(err).Fatalf("error starting the http server")
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownRelease()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("HTTP server shutdown error: %v", err)
	}
	log.Println("HTTP server shut down gracefully")
}
