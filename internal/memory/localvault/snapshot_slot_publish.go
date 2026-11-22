// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	core "github.com/rocareer/rolling-memory/internal/core"
)

const snapshotDiskGuardMargin = int64(64 << 20)

// snapshotFreeDiskBytes is a test seam for the builder's free-space guard.
var snapshotFreeDiskBytes = diskFreeBytes

// checkSnapshotDiskSpace refuses a build that cannot hold the next generation
// plus a margin. An unknown free-space probe is fail-open.
func (v *Vault) checkSnapshotDiskSpace() error {
	if v == nil {
		return nil
	}
	dbPath := v.mainDBPath()
	if strings.TrimSpace(dbPath) == "" {
		return nil
	}
	free, err := snapshotFreeDiskBytes(filepath.Dir(dbPath))
	if err != nil || free <= 0 {
		return nil
	}
	size := int64(0)
	if csr, gen, units, docs, _, ok := resolvePublishedSnapshotPaths(dbPath); ok {
		for _, path := range []string{csr, gen, units, docs} {
			if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
				size += info.Size()
			}
		}
	}
	required := size*2 + snapshotDiskGuardMargin
	if free < required {
		return fmt.Errorf("localvault: insufficient free disk space: need %d bytes, have %d", required, free)
	}
	return nil
}

// publishCorpusSlot writes one immutable generation directory and flips the
// CURRENT pointer. The previous generation is retained as PREV for readers that
// still hold its mmap; older generations are pruned after the flip.
func (v *Vault) publishCorpusSlot(ctx context.Context, dbPath, signature string, changeSeq, docsRev int64, eng *core.Engine, docs []vaultDocEntry) (string, error) {
	if v == nil || strings.TrimSpace(dbPath) == "" || strings.TrimSpace(signature) == "" || eng == nil {
		return "", errors.New("localvault: snapshot publish requires a vault, signature and engine")
	}
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	slot := snapshotSlotName(changeSeq, docsRev, time.Now().UnixNano())
	dir := snapshotSlotDir(dbPath, slot)
	temp := dir + ".tmp"
	_ = os.RemoveAll(temp)
	if err := os.MkdirAll(temp, 0o700); err != nil {
		return "", err
	}
	cleanup := func() { _ = os.RemoveAll(temp) }
	csrPath := filepath.Join(temp, snapshotSlotCSR)
	if err := core.WriteDiskClusterCSR(csrPath, eng); err != nil {
		cleanup()
		return "", err
	}
	// The build sidecar is an optimisation: a failure leaves the next builder on
	// the full materialisation path and must not block the read-side publish.
	_ = core.WriteClusterBuildAux(filepath.Join(temp, snapshotSlotAgg), eng)
	if ctx != nil && ctx.Err() != nil {
		cleanup()
		return "", ctx.Err()
	}
	if err := writeSnapshotFile(filepath.Join(temp, snapshotSlotGen), []byte(strings.TrimSpace(signature)+"\n")); err != nil {
		cleanup()
		return "", err
	}
	if err := v.persistCanonicalUnitRevisionsTo(filepath.Join(temp, snapshotSlotUnits)); err != nil {
		cleanup()
		return "", err
	}
	if docs != nil {
		// The docs sidecar is an optimisation; a failure leaves the reader on
		// the canonical scan and must not block the cluster publication.
		_ = writeCorpusDocsSidecarTo(filepath.Join(temp, snapshotSlotDocs), signature, changeSeq, docs)
	}
	if err := syncSnapshotDir(temp); err != nil {
		cleanup()
		return "", err
	}
	if err := os.Rename(temp, dir); err != nil {
		cleanup()
		return "", err
	}
	if err := syncSnapshotDir(snapshotSlotsDir(dbPath)); err != nil {
		return "", err
	}
	if err := publishSnapshotSlot(dbPath, slot); err != nil {
		return "", err
	}
	return slot, nil
}

func writeSnapshotFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	handle, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	return handle.Sync()
}

func syncSnapshotDir(path string) error {
	handle, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	return handle.Sync()
}
