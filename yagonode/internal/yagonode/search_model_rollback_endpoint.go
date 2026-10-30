package yagonode

import (
	"context"
	"fmt"
	"net/http"
)

type revertibleModelCatalog interface {
	Rollback(context.Context) (bool, error)
}

type searchModelRollbackMessages struct {
	catalogUnavailable string
	rollbackFailure    string
	historyEmpty       string
}

type searchModelRollbackEndpoint struct {
	catalog          revertibleModelCatalog
	messages         searchModelRollbackMessages
	writeActiveModel func(http.ResponseWriter)
}

func (endpoint searchModelRollbackEndpoint) ServeHTTP(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}
	if endpoint.catalog == nil {
		http.Error(w, endpoint.messages.catalogUnavailable, http.StatusServiceUnavailable)

		return
	}
	rolledBack, err := endpoint.catalog.Rollback(r.Context())
	if err != nil {
		http.Error(
			w,
			fmt.Sprintf("%s: %v", endpoint.messages.rollbackFailure, err),
			http.StatusInternalServerError,
		)

		return
	}
	if !rolledBack {
		http.Error(w, endpoint.messages.historyEmpty, http.StatusConflict)

		return
	}
	endpoint.writeActiveModel(w)
}
