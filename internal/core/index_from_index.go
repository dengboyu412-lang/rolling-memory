// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// 本件是「从当前索引重建新索引」的通用路径：当簇状态尚未物化（写路径惰性化）时，
// 不再强制全库物化，而是直接以旧索引的逐单元映射与倒排为源，按新的单元下标重建。
//
// 与 BuildIndex 的语义逐位一致：ID 字典序决定下标、DF 按单元计数、倒排按单元升序、
// 逐单元的位置/位宽/标志/族标签共享旧映射（只读），finalizeMasses 统一收尾。
package core

import "sort"

// indexFromIndexApplicable 报告旧索引是否带有完整的逐单元映射与倒排（未被磁盘段释放）。
func indexFromIndexApplicable(old *Index) bool {
	return old != nil && old.N > 0 && len(old.UnitIDs) == old.N &&
		old.Postings != nil &&
		len(old.UnitPositions) == old.N && len(old.UnitBits) == old.N &&
		len(old.UnitFlags) == old.N && len(old.UnitGroups) == old.N &&
		len(old.DocTerms) == old.N
}

// postingOccAt 在按单元升序的倒排里查找 unit 的出现次数（tf）。
func postingOccAt(posts []Posting, unit int32) int {
	i := sort.Search(len(posts), func(i int) bool { return posts[i].Unit >= unit })
	if i < len(posts) && posts[i].Unit == unit {
		return int(posts[i].Occ)
	}
	return 1
}

// buildIndexFromIndex 以旧索引为源重建新索引：单元下标按 ID 字典序重新分配，
// 未变单元的映射整段共享，只重建倒排与词项统计。
func buildIndexFromIndex(old *Index, delta []Doc, drop map[string]struct{}) *Index {
	latest := make(map[string]Doc, len(delta))
	for _, doc := range delta {
		if doc.UnitID == "" {
			continue
		}
		if drop != nil {
			if _, gone := drop[doc.UnitID]; gone {
				continue
			}
		}
		latest[doc.UnitID] = doc
	}
	idSet := make(map[string]struct{}, old.N+len(latest))
	for _, id := range old.UnitIDs {
		if id == "" {
			continue
		}
		if drop != nil {
			if _, gone := drop[id]; gone {
				continue
			}
		}
		idSet[id] = struct{}{}
	}
	for id := range latest {
		idSet[id] = struct{}{}
	}
	if len(idSet) == 0 {
		return nil
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	idx := &Index{
		N:             len(ids),
		UnitIDs:       ids,
		UnitOf:        make(map[string]int32, len(ids)),
		DocMass:       make([]float64, len(ids)),
		DocTerms:      make([][]uint64, len(ids)),
		Mass:          make(map[uint64]float64, len(old.Mass)),
		DF:            make(map[uint64]int, len(old.DF)),
		Postings:      make(map[uint64][]Posting, len(old.Postings)),
		TermBits:      make(map[uint64]uint16, len(old.TermBits)),
		TermFlags:     make(map[uint64]uint8, len(old.TermFlags)),
		UnitBits:      make([]map[uint64]uint16, len(ids)),
		UnitFlags:     make([]map[uint64]uint8, len(ids)),
		UnitRoleExt:   make([]map[uint64]uint8, len(ids)),
		UnitExtFlags:  make([]map[uint64]uint16, len(ids)),
		UnitGroups:    make([]map[uint64]uint64, len(ids)),
		UnitMeta:      make([]DocMeta, len(ids)),
		UnitPositions: make([]map[uint64][]TermPosition, len(ids)),
		MeanBits:      old.MeanBits,
	}
	for h, m := range old.Mass {
		idx.Mass[h] = m
	}
	// DF 必须从零重建：幸存单元与增量单元都会各计一次，直接沿用旧值会翻倍。
	for h, b := range old.TermBits {
		idx.TermBits[h] = b
	}
	for h, f := range old.TermFlags {
		idx.TermFlags[h] = f
	}

	for unit, id := range ids {
		u32 := int32(unit)
		idx.UnitOf[id] = u32
		if doc, ok := latest[id]; ok {
			insertDeltaDocIntoIndex(idx, unit, doc)
			continue
		}
		oldU := old.UnitOf[id]
		idx.UnitMeta[unit] = old.UnitMeta[oldU]
		idx.UnitPositions[unit] = old.UnitPositions[oldU]
		idx.UnitBits[unit] = old.UnitBits[oldU]
		idx.UnitFlags[unit] = old.UnitFlags[oldU]
		idx.UnitRoleExt[unit] = old.UnitRoleExt[oldU]
		idx.UnitExtFlags[unit] = old.UnitExtFlags[oldU]
		idx.UnitGroups[unit] = old.UnitGroups[oldU]
		for _, h := range old.DocTerms[oldU] {
			occ := postingOccAt(old.Postings[h], oldU)
			idx.DF[h]++
			idx.Postings[h] = append(idx.Postings[h], Posting{Unit: u32, Occ: int32(occ)})
		}
	}
	idx.finalizeMasses()
	return idx
}

// insertDeltaDocIntoIndex 把一份新增/覆盖文档按 addTerm 同式写入新索引。
func insertDeltaDocIntoIndex(idx *Index, unit int, doc Doc) {
	u32 := int32(unit)
	idx.UnitMeta[unit] = doc.Meta()
	explicitPos := len(doc.Positions) > 0
	var posMap map[uint64][]TermPosition
	if explicitPos {
		posMap = make(map[uint64][]TermPosition, len(doc.Positions))
		for h, pl := range doc.Positions {
			if len(pl) == 0 {
				if pl != nil {
					posMap[h] = []TermPosition{}
				}
				continue
			}
			cp := make([]TermPosition, len(pl))
			copy(cp, pl)
			posMap[h] = cp
		}
	}
	termCap := len(doc.Terms)
	uBits := make(map[uint64]uint16, termCap)
	uFlags := make(map[uint64]uint8, termCap)
	uRoleExt := make(map[uint64]uint8, termCap)
	uExt := make(map[uint64]uint16, termCap)
	uGroups := make(map[uint64]uint64, termCap)
	for hash, atom := range doc.Terms {
		occ := doc.Counts[hash]
		if occ < 1 {
			occ = 1
		}
		idx.DF[hash]++
		idx.Postings[hash] = append(idx.Postings[hash], Posting{Unit: u32, Occ: int32(occ)})
		if atom.Bits > idx.TermBits[hash] {
			idx.TermBits[hash] = atom.Bits
		}
		if atom.Flags != 0 {
			idx.TermFlags[hash] |= atom.Flags
		}
		uBits[hash] = atom.Bits
		uFlags[hash] |= atom.Flags
		if re := PackTermRoleExt(atom.Role, atom.ExtFlags); re != 0 {
			uRoleExt[hash] = re
		}
		if hi := atom.ExtFlags &^ 0x07; hi != 0 {
			uExt[hash] |= hi
		}
		if atom.Group != 0 {
			uGroups[hash] = atom.Group
		}
		if !explicitPos && atom.Sent > 0 && atom.Slot > 0 {
			if posMap == nil {
				posMap = make(map[uint64][]TermPosition, termCap)
			}
			posMap[hash] = []TermPosition{{Sentence: int(atom.Sent - 1), Slot: int(atom.Slot - 1)}}
		}
	}
	idx.UnitBits[unit] = uBits
	idx.UnitFlags[unit] = uFlags
	if len(uRoleExt) > 0 {
		idx.UnitRoleExt[unit] = uRoleExt
	}
	if len(uGroups) > 0 {
		idx.UnitGroups[unit] = uGroups
	}
	if len(uExt) > 0 {
		idx.UnitExtFlags[unit] = uExt
	}
	if len(posMap) > 0 {
		idx.UnitPositions[unit] = posMap
	}
}
