package corehttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	path "github.com/ipfs/go-path"
	ipath "github.com/ipfs/interface-go-ipfs-core/path"
	"go.uber.org/zap"
)

func (i *gatewayHandler) serveIpnsRecord(ctx context.Context, w http.ResponseWriter, r *http.Request, contentPath ipath.Path, begin time.Time, logger *zap.SugaredLogger) {
	if contentPath.Namespace() != "ipns" {
		err := fmt.Errorf("%s is not an IPNS link", contentPath.String())
		webError(w, err.Error(), err, http.StatusBadRequest)
		return
	}

	key := contentPath.String()
	key = strings.TrimSuffix(key, "/")
	if strings.Count(key, "/") > 2 {
		err := errors.New("cannot find ipns key for subpath")
		webError(w, err.Error(), err, http.StatusBadRequest)
		return
	}

	record, err := i.api.Routing().Get(ctx, key)
	if err != nil {
		webError(w, err.Error(), err, http.StatusInternalServerError)
		return
	}

	// Set Content-Disposition
	var name string
	if urlFilename := r.URL.Query().Get("filename"); urlFilename != "" {
		name = urlFilename
	} else {
		name = path.SplitList(key)[2] + ".ipns-record"
	}
	setContentDispositionHeader(w, name, "attachment")

	w.Header().Set("Content-Type", "application/vnd.ipfs.ipns-record")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	_, _ = w.Write(record)
}
