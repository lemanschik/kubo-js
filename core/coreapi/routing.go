package coreapi

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/ipfs/go-path"
	peer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
)

type RoutingAPI CoreAPI

func (r *RoutingAPI) Get(ctx context.Context, key string) ([]byte, error) {
	dhtKey, err := ensureIPNSKey(key)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	ctx, events := routing.RegisterForQueryEvents(ctx)

	var getErr error

	go func() {
		defer cancel()
		var val []byte
		val, getErr = r.routing.GetValue(ctx, dhtKey)
		if getErr != nil {
			routing.PublishQueryEvent(ctx, &routing.QueryEvent{
				Type:  routing.QueryError,
				Extra: getErr.Error(),
			})
		} else {
			routing.PublishQueryEvent(ctx, &routing.QueryEvent{
				Type:  routing.Value,
				Extra: base64.StdEncoding.EncodeToString(val),
			})
		}
	}()

	for e := range events {
		if e.Type == routing.Value {
			return base64.StdEncoding.DecodeString(e.Extra)
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return nil, errors.New("key not found")
}

// Shamelessly (mostly) copied from commands/routing.go
func ensureIPNSKey(s string) (string, error) {
	parts := path.SplitList(s)
	if len(parts) != 3 ||
		parts[0] != "" ||
		parts[1] != "ipns" {
		return "", errors.New("invalid key")
	}

	k, err := peer.Decode(parts[2])
	if err != nil {
		return "", err
	}
	return path.Join(append(parts[:2], string(k))), nil
}
