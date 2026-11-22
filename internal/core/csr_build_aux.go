// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// A published .zcsr carries the read-side cluster state (documents, postings,
// phrases, buckets). It intentionally omits clusterAggregates because readers
// never need them. A builder that reopens a published generation and extends
// it in place does need them: without the family df/cluster/partner counters
// extendForDelta cannot update term weights, and the engine falls back to a
// full materialisation.
//
// The aux sidecar persists exactly that write-side sufficient statistic. It
// contains term hashes, counters and root snapshots only: no text, no document
// metadata and no business fields.
const (
	clusterBuildAuxMagic   = "RMBA"
	clusterBuildAuxVersion = 1
	clusterBuildAuxMaxSize = 512 << 20
)

type clusterBuildAux struct {
	Magic            string
	Version          uint32
	UnitIDs          []string
	ClusterN         int32
	TermPartnerStats bool
	Extended         bool
	TermParentKeys   []uint64
	TermParentVals   []uint64
	Fams             map[uint64]clusterFamilyAux
	TermPartners     map[uint64][]uint64
	TermPartnerCnt   map[uint64][]int32
}

type clusterFamilyAux struct {
	DF            int32
	MultiDocCount int32
	Weight        float64
	ClusterKey    []int64
	ClusterCnt    []int32
	PartnerKey    []uint64
	PartnerCnt    []int32
}

// WriteClusterBuildAux atomically writes the write-side sufficient statistics
// of eng's current generation next to its .zcsr. The file is an optimisation:
// a missing or unusable aux sidecar only costs the next build a full cluster
// materialisation, never correctness.
func WriteClusterBuildAux(path string, eng *Engine) error {
	if path == "" {
		return errors.New("rm/core: empty build-aux path")
	}
	if eng == nil || eng.Index == nil {
		return errors.New("rm/core: build aux requires an engine")
	}
	st := eng.ensureClusterState()
	if st == nil || st.agg == nil {
		return errors.New("rm/core: engine has no materialised cluster aggregates")
	}
	agg := st.agg
	if len(agg.unitIDs) != eng.Index.N {
		return fmt.Errorf("rm/core: aggregate unit list has %d entries, index has %d", len(agg.unitIDs), eng.Index.N)
	}
	payload := clusterBuildAux{
		Magic:            clusterBuildAuxMagic,
		Version:          clusterBuildAuxVersion,
		UnitIDs:          append([]string(nil), agg.unitIDs...),
		ClusterN:         agg.clusterN,
		TermPartnerStats: termPartnerStats,
		Extended:         agg.extended,
		TermParentKeys:   cloneUint64Slice(agg.termParentKeys),
		TermParentVals:   cloneUint64Slice(agg.termParentVals),
		Fams:             make(map[uint64]clusterFamilyAux, len(agg.fams)),
	}
	for hash, fam := range agg.fams {
		if fam == nil {
			continue
		}
		payload.Fams[hash] = clusterFamilyAux{
			DF:            fam.df,
			MultiDocCount: fam.multiDocCount,
			Weight:        fam.weight,
			ClusterKey:    cloneInt64Slice(fam.clusterKey),
			ClusterCnt:    cloneInt32Slice(fam.clusterCnt),
			PartnerKey:    cloneUint64Slice(fam.partnerKey),
			PartnerCnt:    cloneInt32Slice(fam.partnerCnt),
		}
	}
	if termPartnerStats && len(agg.termPartners) > 0 {
		payload.TermPartners = make(map[uint64][]uint64, len(agg.termPartners))
		payload.TermPartnerCnt = make(map[uint64][]int32, len(agg.termPartners))
		for host, partners := range agg.termPartners {
			payload.TermPartners[host] = cloneUint64Slice(partners)
			payload.TermPartnerCnt[host] = cloneInt32Slice(agg.termPartnerCnt[host])
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".rmba-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	if err := gob.NewEncoder(temp).Encode(&payload); err != nil {
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err := os.Chmod(tempName, 0o600); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	return nil
}

// AttachClusterBuildAux rehydrates the write-side sufficient statistics onto an
// engine opened from a published .zcsr. It validates the unit order and the
// aggregation mode, so a sidecar from a different generation can never be
// applied silently.
func AttachClusterBuildAux(path string, eng *Engine) error {
	if path == "" {
		return errors.New("rm/core: empty build-aux path")
	}
	if eng == nil || eng.Index == nil {
		return errors.New("rm/core: build aux requires an engine")
	}
	st := eng.clusterState
	if st == nil {
		return errors.New("rm/core: engine has no cluster state to attach aggregates to")
	}
	if st.agg != nil {
		return errors.New("rm/core: engine already carries cluster aggregates")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() <= 0 || info.Size() > clusterBuildAuxMaxSize {
		return fmt.Errorf("rm/core: build aux size %d out of range", info.Size())
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var payload clusterBuildAux
	if err := gob.NewDecoder(io.LimitReader(f, clusterBuildAuxMaxSize+1)).Decode(&payload); err != nil {
		return fmt.Errorf("rm/core: decode build aux: %w", err)
	}
	if payload.Magic != clusterBuildAuxMagic {
		return fmt.Errorf("rm/core: build aux magic %q mismatch", payload.Magic)
	}
	if payload.Version != clusterBuildAuxVersion {
		return fmt.Errorf("rm/core: build aux version %d unsupported", payload.Version)
	}
	if payload.TermPartnerStats != termPartnerStats {
		return errors.New("rm/core: build aux aggregation mode differs from the current mode")
	}
	if payload.ClusterN != int32(st.clusterN) {
		return fmt.Errorf("rm/core: build aux clusterN %d differs from state %d", payload.ClusterN, st.clusterN)
	}
	if len(payload.UnitIDs) != eng.Index.N {
		return fmt.Errorf("rm/core: build aux has %d units, engine has %d", len(payload.UnitIDs), eng.Index.N)
	}
	for i, id := range payload.UnitIDs {
		if id != eng.Index.UnitIDs[i] {
			return fmt.Errorf("rm/core: build aux unit %d is %q, engine has %q", i, id, eng.Index.UnitIDs[i])
		}
	}
	agg := &clusterAggregates{
		unitIDs:        payload.UnitIDs,
		fams:           make(map[uint64]*clusterFamilyAgg, len(payload.Fams)),
		clusterN:       payload.ClusterN,
		termParentKeys: payload.TermParentKeys,
		termParentVals: payload.TermParentVals,
		termPartners:   payload.TermPartners,
		termPartnerCnt: payload.TermPartnerCnt,
		extended:       payload.Extended,
	}
	for hash, fam := range payload.Fams {
		agg.fams[hash] = &clusterFamilyAgg{
			df:            fam.DF,
			multiDocCount: fam.MultiDocCount,
			weight:        fam.Weight,
			clusterKey:    fam.ClusterKey,
			clusterCnt:    fam.ClusterCnt,
			partnerKey:    fam.PartnerKey,
			partnerCnt:    fam.PartnerCnt,
		}
	}
	st.agg = agg
	return nil
}

func cloneUint64Slice(in []uint64) []uint64 {
	if len(in) == 0 {
		return nil
	}
	return append([]uint64(nil), in...)
}

func cloneInt64Slice(in []int64) []int64 {
	if len(in) == 0 {
		return nil
	}
	return append([]int64(nil), in...)
}

func cloneInt32Slice(in []int32) []int32 {
	if len(in) == 0 {
		return nil
	}
	return append([]int32(nil), in...)
}
