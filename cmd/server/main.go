package main

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func main() {
	r := chi.NewRouter()

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode("Hello World")
	})

	http.ListenAndServe(":8080", r)
}
