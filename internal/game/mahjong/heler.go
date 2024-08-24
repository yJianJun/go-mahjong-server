package mahjong

import (
	"go-mahjong-server/protocol"
)

func NewStats(indexes ...Indexes) *Stats {
	ts := &Stats{}
	ts.FromIndex(indexes...)
	return ts
}

// 是否是清一色, 返回是否是清一色, 包括万、筒、条、字牌的情况
func isQingYiSe(ms *Stats) bool {
	var flag byte

	for i, v := range ms {
		if v == 0 {
			continue
		}

		if i > 20 {
			flag |= 1 << 2
		} else if i > 10 {
			flag |= 1 << 1
		} else {
			flag |= 1
		}

	}

	return flag == 4 || flag == 2 || flag == 1
}

// 7对, 返回是否是七对, 以及包含杠的个数
func isQiDui(ms *Stats) bool {
	// 麻将对的计数器
	pairCount := 0

	// 它迭代字节切片（统计）
	for _, v := range ms {
		// 如果某个牌类型的计数为零，则忽略该类型并继续前进
		if v == 0 {
			continue
		}

		// 检查某种牌类型的计数是 2 还是 4（分别可以形成一对或两对）
		// 如果不是，它立即返回 false ，表示麻将无法形成所需的“七对”
		if v != 2 && v != 4 {
			return false
		}

		// 如果计数确实是 2 或 4，则相应地增加对计数
		if v == 2 {
			pairCount++
		} else if v == 4 {
			pairCount += 2
		}
	}

	// 计算完所有对后，检查是否正好有 7 对。如果是则返回 true，否则返回 false
	return pairCount == 7
}

// 大对子, 返回是否是大对子,包括所有的牌型
func isDaDui(ms *Stats) bool {
	counter := 0

	for _, v := range ms {
		if v == 0 {
			continue
		}

		// 有单牌不可能是大对子
		if v < 2 {
			return false
		}

		if v >= 3 {
			counter++
		}
	}

	return counter == 4
}

// 检查大对子和七对是不是只包含258
func is258(ms *Stats) bool {
	for index, v := range ms {
		if v == 0 {
			continue
		}

		switch mod := index % 10; mod {
		case 2, 5, 8:
			continue
		default:
			return false
		}
	}
	return true
}

// 胡牌时, 所有牌没有1和9
func isZhongzhang(ms *Stats) bool {
	for index, v := range ms {
		if v == 0 {
			continue
		}

		if mod := index % 10; mod == 1 || mod == 9 {
			return false
		}
	}
	return true
}

// 是否是夹心五, 返回是否是夹心五, 只判断万, 筒, 条的情况, 不包括字牌
func isJiaxin(ctx *Context, onHand Indexes) bool {
	index := IndexFromID(ctx.NewDrawingID)
	if id := ctx.NewOtherDiscardID; id != protocol.OptypeIllegal && id >= 0 {
		index = IndexFromID(ctx.NewOtherDiscardID)
	}

	// 5,15,25
	if index%10 != 5 {
		return false
	}

	//默认胡5条
	willRemoveTiles := Indexes{4, 5, 6}
	if index == 15 {
		willRemoveTiles = Indexes{14, 15, 16}
	} else if index == 25 {
		willRemoveTiles = Indexes{24, 25, 26}
	}

	//卡5星判断规则:
	//胡的牌必须是5条、5同
	//移除4,5,6 OR 14,15,16 OR 24,25,26后仍然可以和牌

	marker := func(tiles Indexes, r int) {
		for i := 0; i < len(tiles); i++ {
			//只移除第一个
			if tiles[i] == r {
				tiles[i] = IllegalIndex
				return
			}
		}

	}

	temp := make(Indexes, len(onHand))

	for i := 0; i < len(onHand); i++ {
		temp[i] = onHand[i]
	}

	for _, t := range willRemoveTiles {
		marker(temp, t)
	}

	var tiles Indexes

	for _, t := range temp {
		if t == IllegalIndex {
			continue
		}

		tiles = append(tiles, t)
	}

	return CheckWin(tiles)
}

// min 返回给定字节值中的最小值。
func min(n byte, ns ...byte) byte {
	m := n
	for _, x := range ns {
		if x < m {
			m = x
		}
	}
	return m
}

// isYJ 判断是不是幺九, 返回是否是幺九
// 1. 排除1和9的刻字，如果有不是1和9的刻子就不可能是幺九
func isYJ(onHand, pongkong Indexes) bool {
	pg := NewStats(pongkong)
	for index, count := range pg {
		if count < 3 {
			continue
		}
		if m := index % 10; m != 1 && m != 9 {
			return false
		}
	}

	ms := NewStats(onHand)
	//println(ms.String())

	// 清理顺子，如果有1就删除2/3，如果有9就删除7/8，如果剩下的对子是1/9则成功
	yao := []byte{1, 11, 21}
	for _, y := range yao {
		if count1 := ms[y]; count1 > 0 {
			count2 := ms[y+1]
			count3 := ms[y+2]
			c := min(count1, count2, count3)

			ms[y] -= c
			ms[y+1] -= c
			ms[y+2] -= c
		}
	}

	jiu := []byte{9, 19, 29}
	for _, j := range jiu {
		if count1 := ms[j]; count1 > 0 {
			count2 := ms[j-1]
			count3 := ms[j-2]
			c := min(count1, count2, count3)

			ms[j] -= c
			ms[j-1] -= c
			ms[j-2] -= c
		}
	}

	//println(ms.String())

	// 清理刻子
	for index, count := range ms {
		if count < 3 {
			continue
		}

		m := index % 10
		// 有不是1/9的刻子，不可能是幺九
		if m != 1 && m != 9 {
			return false
		}

		ms[index] -= 3
	}

	// 剩下的对子也只能是幺九
	for index, count := range ms {
		if count > 0 && count != 2 {
			return false
		}
		if count != 2 {
			continue
		}
		m := index % 10
		if m != 1 && m != 9 {
			return false
		}
	}

	//println(ms.String())
	return true
}

// gangCount 函数计算玩家手牌中有多少个杠
func gangCount(ms *Stats) int {
	counter := 0
	for _, v := range ms {
		if v == 4 {
			counter++
		}
	}
	return counter
}

// CanHu 检查玩家是否可以通过丢弃特定牌来获胜。
// 它将丢弃的牌附加到玩家的手上，然后调用 CheckWin
// 判断玩家是否有一手获胜牌。
// 如果 CheckWin 返回 true，则 CanHu 返回 true；否则，返回 false。
func CanHu(onHand Indexes, discard int) bool {
	onHand = append(onHand, discard)
	return CheckWin(onHand)
}

// IsTing 检查向 onHand 切片添加一个牌是否会导致一手获胜。
// 它创建 onHand 的克隆并向其中添加每个可能的牌。然后它调用 CheckWin 函数来检查新手牌是否是获胜手牌。
// 如果至少有一次加法导致获胜，则返回 true。否则，返回 false。
// 范围：
// - onHand：当前手牌的牌表示为整数切片。
// 返回：
// - bool：如果向手牌添加一张牌会导致获胜，则返回 true，否则返回 false。
func IsTing(onHand Indexes) bool {
	clone := make(Indexes, len(onHand)+1)
	for i := 0; i <= MaxTileIndex; i++ {
		if i%10 == 0 {
			continue
		}
		copy(clone, onHand)
		clone[len(onHand)] = i
		if CheckWin(clone) {
			return true
		}
	}
	return false
}

// 返回所有的听牌
func TingTiles(onHand Indexes) Indexes {
	// 为新牌腾出空间
	clone := make(Indexes, len(onHand)+1)
	// 存储听牌的索引
	rts := make(Indexes, 0)

	// 遍历所有可能的牌索引
	for i := 0; i <= MaxTileIndex; i++ {
		// 跳过 10 秒，因为它们无效
		if i%10 == 0 {
			continue
		}
		// 使用当前索引创建一手新牌作为额外牌
		copy(clone, onHand)
		clone[len(onHand)] = i
		// 如果新手牌是获胜手牌，则附加当前索引
		if CheckWin(clone) {
			rts = append(rts, i)
		}
	}
	// 返回所有听牌
	return rts
}
