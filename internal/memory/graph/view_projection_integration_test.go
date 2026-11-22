// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package graph_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/graphmodel"
	"github.com/rocareer/rolling-memory/internal/memory/graph"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	_ "modernc.org/sqlite"
)

// The projection drops corpus-background word families and every phrase that
// touches one, while the unit/cluster structure and the underlying view stay
// intact. Retrieval does not consume this view, so the filter cannot move a
// recall score.
func TestGraphViewDropsBackgroundWordsAndPhrases(t *testing.T) {
	db, err := sql.Open("sqlite", "file:graph_projection_bg?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, err := localvault.NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	vault.SetIdentity(localvault.VaultIdentity{Locus: "test"})
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("ep_bg_%02d", i)
		if err := vault.SaveLocalPlaintext(id, 1700000000+int64(i), "common filler words here", nil, localvault.IngestResponse{
			Layer:      "episodic",
			AddressURI: "C6:L1:LOCAL#" + id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := vault.SaveLocalPlaintext("ep_rare", 1700001000, "rare unique signal", nil, localvault.IngestResponse{
		Layer:      "episodic",
		AddressURI: "C6:L1:LOCAL#ep_rare",
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := graph.NewBuilder(vault).View(context.Background(), graphmodel.ViewRequest{Limit: 256})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stats.DisplayFilteredWords == 0 {
		t.Fatalf("expected background words to be filtered, stats=%+v", resp.Stats)
	}
	if resp.Stats.DisplayFilteredPhrases == 0 {
		t.Fatalf("expected background phrases to be filtered, stats=%+v", resp.Stats)
	}
	for _, node := range resp.Nodes {
		if node.Kind == graphmodel.KindWord && strings.EqualFold(strings.TrimSpace(node.Label), "common") {
			t.Fatalf("background word node survived: %+v", node)
		}
		if node.Kind == graphmodel.KindPhrase && strings.Contains(strings.ToLower(node.Label), "common") {
			t.Fatalf("background phrase node survived: %+v", node)
		}
	}
	var rareSeen bool
	for _, node := range resp.Nodes {
		if node.Kind == graphmodel.KindWord && strings.EqualFold(strings.TrimSpace(node.Label), "rare") {
			rareSeen = true
			break
		}
	}
	if !rareSeen {
		t.Fatalf("rare word node missing after projection")
	}
}
