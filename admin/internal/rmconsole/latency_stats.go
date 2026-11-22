// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package rollingmemory

import "sort"

// recallLatencyStats 从最近若干次成功召回的墙钟样本里取稳健统计。
//
// 背景：看板上的「召回延迟」取自最近若干次成功 MCP 召回在记忆服务端的处理耗时
// （账本自记，所有宿主同一口径），单点样本随负载与查询宽度抖动很大；把中位数作为
// 主读数、峰值与最近一次放进说明，才能如实呈现「典型多快」，而不是把一次尖峰当成常态。
// 样本为空时全部为 0，不伪造读数。
func recallLatencyStats(latencies []int) (last, avg, p50, max int) {
	if len(latencies) == 0 {
		return 0, 0, 0, 0
	}
	last = latencies[len(latencies)-1]
	sum := 0
	for _, value := range latencies {
		sum += value
		if value > max {
			max = value
		}
	}
	avg = sum / len(latencies)
	sorted := append([]int(nil), latencies...)
	sort.Ints(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		p50 = sorted[middle]
	} else {
		p50 = (sorted[middle-1] + sorted[middle]) / 2
	}
	return last, avg, p50, max
}

// recallLatencyTrend 计算主读数（中位数）相对近期均值的百分比变化（一位小数）。
func recallLatencyTrend(p50, avg int) float64 {
	if avg <= 0 {
		return 0
	}
	diff := float64(p50-avg) * 100.0 / float64(avg)
	return float64(int(diff*10)) / 10
}
