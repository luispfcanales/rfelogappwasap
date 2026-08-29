package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/luispfcanales/rfelogappwasap/internal/api"
	"github.com/luispfcanales/rfelogappwasap/internal/whatsapp"
)

func main() {
	ctx := context.Background()

	waClient, err := whatsapp.New(ctx, "wasession.db")
	if err != nil {
		log.Fatal(err)
	}
	defer waClient.Disconnect()

	router := api.NewRouter(waClient)

	go func() {
		fmt.Println("API escuchando en :8090  (POST /send)")
		if err := http.ListenAndServe(":8090", router); err != nil {
			log.Fatal(err)
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
