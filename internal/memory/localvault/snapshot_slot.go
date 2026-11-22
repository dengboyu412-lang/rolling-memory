// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	snapshotSlotsDirSuffix = ".snapshots"
	snapshotCurrentName    = "CURRENT"
	snapshotPrevName       = "PREV"
	snapshotSlotCSR        = "snap.zcsr"
	snapshotSlotGen        = "snap.gen"
	snapshotSlotUnits      = "snap.units"
	snapshotSlotDocs       = "snap.docs"
	snapshotSlotAgg        = "snap.agg"
	snapshotPointerPrefix  = "v1 "
)

func snapshotSlotsDir(dbPath string) string {
	return dbPath + snapshotSlotsDirSuffix
}

func snapshotCurrentPath(dbPath string) string {
	return filepath.Join(snapshotSlotsDir(dbPath), snapshotCurrentName)
}

func snapshotPrevPath(dbPath string) string {
	return filepath.Join(snapshotSlotsDir(dbPath), snapshotPrevName)
}

func snapshotSlotDir(dbPath, slot string) string {
	return filepath.Join(snapshotSlotsDir(dbPath), slot)
}

func snapshotSlotPaths(dbPath, slot string) (csr, gen, units, docs string) {
	dir := snapshotSlotDir(dbPath, slot)
	return filepath.Join(dir, snapshotSlotCSR), filepath.Join(dir, snapshotSlotGen),
		filepath.Join(dir, snapshotSlotUnits), filepath.Join(dir, snapshotSlotDocs)
}

func snapshotFilePresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func readSnapshotPointer(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, snapshotPointerPrefix) {
		return "", false
	}
	slot := strings.TrimSpace(strings.TrimPrefix(line, snapshotPointerPrefix))
	if slot == "" || slot == "." || slot == ".." || strings.ContainsAny(slot, `/\`) {
		return "", false
	}
	return slot, true
}

func writeSnapshotPointer(path, slot string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".pointer-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.WriteString(snapshotPointerPrefix + slot + "\n"); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	return nil
}

// activeSnapshotSlot returns the slot CURRENT points to when its core files are
// present. A malformed pointer or a partial slot is treated as absent.
func activeSnapshotSlot(dbPath string) (string, bool) {
	slot, ok := readSnapshotPointer(snapshotCurrentPath(dbPath))
	if !ok {
		return "", false
	}
	csr, gen, _, _ := snapshotSlotPaths(dbPath, slot)
	if !snapshotFilePresent(csr) || !snapshotFilePresent(gen) {
		return "", false
	}
	return slot, true
}

// resolvePublishedSnapshotPaths prefers the CURRENT slot and falls back to the
// legacy single-slot files for vaults that have not been rebuilt yet.
func resolvePublishedSnapshotPaths(dbPath string) (csr, gen, units, docs, slot string, ok bool) {
	if strings.TrimSpace(dbPath) == "" {
		return "", "", "", "", "", false
	}
	if active, activeOK := activeSnapshotSlot(dbPath); activeOK {
		csr, gen, units, docs = snapshotSlotPaths(dbPath, active)
		return csr, gen, units, docs, active, true
	}
	legacyCSR, legacyGen := vaultCSRPaths(dbPath)
	if !snapshotFilePresent(legacyCSR) || !snapshotFilePresent(legacyGen) {
		return "", "", "", "", "", false
	}
	return legacyCSR, legacyGen, vaultCSRUnitPaths(dbPath), corpusDocsSidecarPath(dbPath), "", true
}

// PublishedSnapshotExists is the package-level probe used by status surfaces
// that do not open a vault.
func PublishedSnapshotExists(dbPath string) bool {
	_, _, _, _, _, ok := resolvePublishedSnapshotPaths(dbPath)
	return ok
}

// PublishedSnapshotDocsPath returns the docs sidecar path of the active
// generation, if one is published.
func PublishedSnapshotDocsPath(dbPath string) (string, bool) {
	_, _, _, docs, _, ok := resolvePublishedSnapshotPaths(dbPath)
	return docs, ok
}

// publishSnapshotSlot makes slot the active generation and records the previous
// generation for rollback/readers already holding it.
func publishSnapshotSlot(dbPath, slot string) error {
	if strings.TrimSpace(dbPath) == "" || strings.TrimSpace(slot) == "" {
		return errors.New("localvault: snapshot publish requires a slot")
	}
	previous, _ := readSnapshotPointer(snapshotCurrentPath(dbPath))
	if previous != "" && previous != slot {
		_ = writeSnapshotPointer(snapshotPrevPath(dbPath), previous)
	}
	if err := writeSnapshotPointer(snapshotCurrentPath(dbPath), slot); err != nil {
		return err
	}
	keep := map[string]struct{}{slot: {}}
	if previous != "" {
		keep[previous] = struct{}{}
	}
	pruneSnapshotSlots(dbPath, keep)
	return nil
}

// pruneSnapshotSlots removes generations outside the retained set. Deletion of
// an mmap'd slot is safe on Unix; Windows failures are retried by later builds.
func pruneSnapshotSlots(dbPath string, keep map[string]struct{}) {
	dir := snapshotSlotsDir(dbPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".tmp") {
			// A crashed build leaves a temp directory; old ones are removed.
			info, statErr := entry.Info()
			if statErr == nil && time.Since(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(filepath.Join(dir, name))
			}
			continue
		}
		if _, retained := keep[name]; retained {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, name))
	}
}

// snapshotSlotName is monotonic by change sequence and unique per build.
func snapshotSlotName(changeSeq, docsRev int64, nonce int64) string {
	return fmt.Sprintf("%016x-%016x-%x", changeSeq, docsRev, nonce)
}
