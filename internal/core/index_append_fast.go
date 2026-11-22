// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// 本件是索引的追加式重建快速路径：当新增单元的 UnitID 全部排在既有单元之后、
// 且没有删除时，新索引与旧索引的单元下标一一对应，因此逐单元的位置/位宽/标志/
// 族标签映射可以整段共享，倒排只需按词追加。
//
// 与 BuildIndex 的全量语义逐位一致：DF 按单元计数、倒排按单元升序、TermBits 取最大、
// TermFlags 取或、UnitGroups/UnitPositions 与 addTerm 同式；finalizeMasses 负责
// Mass/DocTerms/DocMass 与缺失族标签的补齐。
package core

import "sort"

// uniqueDeltaIDs 报告增量中的 UnitID 是否互不重复（重复以最后一条为准，走通用路径）。
func uniqueDeltaIDs(delta []Doc) bool {
	seen := make(map[string]struct{}, len(delta))
	for _, doc := range delta {
		if doc.UnitID == "" {
			return false
		}
		if _, dup := seen[doc.UnitID]; dup {
			return false
		}
		seen[doc.UnitID] = struct{}{}
	}
	return true
}

// appendAtEndApplicable 报告增量是否满足追加式快速路径的前置条件。
func appendAtEndApplicable(old *Index, delta []Doc) bool {
	if old == nil || old.N == 0 || len(old.UnitIDs) != old.N || len(delta) == 0 {
		return false
	}
	// 逐单元映射与倒排必须仍在内存中：磁盘段（.zcsr）释放后，幸存单元的族标签、
	// 位宽、标志与位置只能从 CSR 读取，须走通用路径。
	if old.Postings == nil || len(old.UnitPositions) != old.N || len(old.UnitBits) != old.N ||
		len(old.UnitFlags) != old.N || len(old.UnitGroups) != old.N {
		return false
	}
	last := old.UnitIDs[old.N-1]
	for _, doc := range delta {
		if doc.UnitID == "" {
			return false
		}
		if doc.UnitID <= last {
			return false
		}
		if _, exists := old.LookupUnit(doc.UnitID); exists {
			return false
		}
	}
	return true
}

// buildIndexAppendAtEnd 追加式重建索引：共享未变单元的映射与倒排切片，
// 只把新单元写入末尾。delta 的 UnitID 必须严格大于既有最后一个 ID 且互不重复。
func buildIndexAppendAtEnd(old *Index, delta []Doc) *Index {
	nNew := len(delta)
	n := old.N + nNew
	idx := &Index{
		N:             n,
		UnitIDs:       make([]string, 0, n),
		UnitOf:        make(map[string]int32, n),
		DocMass:       make([]float64, n),
		DocTerms:      make([][]uint64, n),
		Mass:          make(map[uint64]float64, len(old.Mass)),
		DF:            make(map[uint64]int, len(old.DF)),
		Postings:      make(map[uint64][]Posting, len(old.Postings)),
		TermBits:      make(map[uint64]uint16, len(old.TermBits)),
		TermFlags:     make(map[uint64]uint8, len(old.TermFlags)),
		UnitBits:      make([]map[uint64]uint16, n),
		UnitFlags:     make([]map[uint64]uint8, n),
		UnitRoleExt:   make([]map[uint64]uint8, n),
		UnitExtFlags:  make([]map[uint64]uint16, n),
		UnitGroups:    make([]map[uint64]uint64, n),
		UnitMeta:      make([]DocMeta, n),
		UnitPositions: make([]map[uint64][]TermPosition, n),
		MeanBits:      old.MeanBits,
	}
	idx.UnitIDs = append(idx.UnitIDs, old.UnitIDs...)
	for i, id := range idx.UnitIDs {
		idx.UnitOf[id] = int32(i)
	}
	// 未变单元：映射整段共享（只读），位置表同样共享。
	copy(idx.UnitBits, old.UnitBits)
	copy(idx.UnitFlags, old.UnitFlags)
	copy(idx.UnitRoleExt, old.UnitRoleExt)
	copy(idx.UnitExtFlags, old.UnitExtFlags)
	copy(idx.UnitGroups, old.UnitGroups)
	copy(idx.UnitMeta, old.UnitMeta)
	copy(idx.UnitPositions, old.UnitPositions)
	for h, m := range old.Mass {
		idx.Mass[h] = m
	}
	for h, d := range old.DF {
		idx.DF[h] = d
	}
	for h, b := range old.TermBits {
		idx.TermBits[h] = b
	}
	for h, f := range old.TermFlags {
		idx.TermFlags[h] = f
	}
	// 未变单元的倒排切片共享；只有被新单元触及的词才复制后追加。
	for h, ps := range old.Postings {
		idx.Postings[h] = ps
	}

	order := make([]int, nNew)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return delta[order[a]].UnitID < delta[order[b]].UnitID })

	for k, di := range order {
		doc := delta[di]
		unit := int32(old.N + k)
		idx.UnitIDs = append(idx.UnitIDs, doc.UnitID)
		idx.UnitOf[doc.UnitID] = unit
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
			ps := idx.Postings[hash]
			np := make([]Posting, len(ps)+1)
			copy(np, ps)
			np[len(ps)] = Posting{Unit: unit, Occ: int32(occ)}
			idx.Postings[hash] = np
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
			// 与 addTerm 同式：无显式位置表时由词项的 (句, 槽) 生成位置。
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
	idx.finalizeMasses()
	return idx
}
