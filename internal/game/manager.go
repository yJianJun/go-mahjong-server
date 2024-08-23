package game

import (
	"go-mahjong-server/protocol"

	"github.com/lonng/nano/scheduler"

	"time"

	"github.com/lonng/nano"
	"github.com/lonng/nano/component"
	"github.com/lonng/nano/session"
	log "github.com/sirupsen/logrus"
)

const kickResetBacklog = 8

var defaultManager = NewManager()

type (

	// Manager 是一个代表游戏管理器的结构体，用于管理玩家会话和状态。
	// 它扩展了 component.Base 结构。
	//
	// 字段：
	// - group：用于玩家之间通信的广播频道
	// - 玩家：所有玩家的map，以他们的 UID 作为键，*Player 指针作为值
	// - chKick：用于从组中删除玩家的通道
	// - chReset：用于在管理器中重置玩家的通道
	// - chRecharge：用于向玩家发送充值信息的通道
	Manager struct {
		component.Base
		group      *nano.Group       // 广播channel
		players    map[int64]*Player // 所有的玩家
		chKick     chan int64        // 退出队列
		chReset    chan int64        // 重置队列
		chRecharge chan RechargeInfo // 充值信息
	}

	// RechargeInfo 是表示用户充值信息的类型。
	// 它有两个字段：
	// - Uid：用户ID
	// - Coin: 充值的金币数量。
	RechargeInfo struct {
		Uid  int64 // 用户ID
		Coin int64 // 房卡数量
	}
)

func NewManager() *Manager {
	return &Manager{
		group:      nano.NewGroup("_SYSTEM_MESSAGE_BROADCAST"),
		players:    map[int64]*Player{},
		chKick:     make(chan int64, kickResetBacklog),
		chReset:    make(chan int64, kickResetBacklog),
		chRecharge: make(chan RechargeInfo, 32),
	}
}

// AfterInit 初始化 Manager 初始化后应该执行的一些操作。
// 它设置一个回调函数，每当会话关闭时都会执行该函数，从而从 Manager 组中删除该会话。
// 此外，它还初始化一个新的计时器，每秒触发该函数。定时器块内的函数是一个循环
// 监听不同的通道并执行相应的操作：
//
// m.chKick：此通道可能会接收应被“踢出”或从组中删除的用户 ID (uid)。
// 如果在 Manager 的玩家集合中找到具有给定 uid 的玩家，则关闭该玩家的会话并写入相应的日志消息。
//
// m.chReset：该通道负责在游戏管理器中“重置”玩家。如果游戏管理员可以找到该玩家并且该玩家当前不在会话中，
// 它继续重置玩家的状态（可能将他们当前的桌子设置为零）并写入适当的日志消息。
// 如果玩家正在会话中，则会记录相关警告消息。
//
// m.chRecharge：在 RechargeInfo 结构中包含 uid 和 Coin 数量，以通知玩家有关硬币变化的信息。
// 如果玩家在线（即具有有效会话），则会向他们推送一条包含硬币找零信息的消息。
//
// 最后，select 块中的 default: case 允许函数在没有通道有任何数据时跳出无限循环。
// 提供的 Go 代码和描述的行为强烈表明 Manager 类型是大型游戏服务器系统的一部分
// 负责管理玩家会话和状态。
func (m *Manager) AfterInit() {
	session.Lifetime.OnClosed(func(s *session.Session) {
		m.group.Leave(s)
	})

	// 处理踢出玩家和重置玩家消息(来自http)
	scheduler.NewTimer(time.Second, func() {
	ctrl:
		for {
			select {
			case uid := <-m.chKick:
				p, ok := defaultManager.player(uid)
				if !ok || p.session == nil {
					logger.Errorf("玩家%d不在线", uid)
				}
				p.session.Close()
				logger.Infof("踢出玩家, UID=%d", uid)

			case uid := <-m.chReset:
				p, ok := defaultManager.player(uid)
				if !ok {
					return
				}
				if p.session != nil {
					logger.Errorf("玩家正在游戏中，不能重置: %d", uid)
					return
				}
				p.desk = nil
				logger.Infof("重置玩家, UID=%d", uid)

			case ri := <-m.chRecharge:
				player, ok := m.player(ri.Uid)
				// 如果玩家在线
				if s := player.session; ok && s != nil {
					s.Push("onCoinChange", &protocol.CoinChangeInformation{Coin: ri.Coin})
				}

			default:
				break ctrl
			}
		}
	})
}

// Login 处理用户登录游戏服务器的逻辑。
// 它需要一个会话和一个 LoginToGameServerRequest 作为参数。
// 如果具有给定UID的玩家当前不在线，则创建一个新玩家，
// 添加到玩家map，并绑定到会话。
// 如果玩家已经在线，则将之前的会话从广播组中删除，
// 之前的会话关闭，新的会话与玩家绑定。
// 最后，玩家被添加到广播组，并且 LoginToGameServerResponse 被发送回会话。
// 如果过程中出现错误，则返回错误。
func (m *Manager) Login(s *session.Session, req *protocol.LoginToGameServerRequest) error {
	uid := req.Uid
	s.Bind(uid)

	log.Infof("玩家: %d登录: %+v", uid, req)
	if p, ok := m.player(uid); !ok {
		log.Infof("玩家: %d不在线，创建新的玩家", uid)
		p = newPlayer(s, uid, req.Name, req.HeadUrl, req.IP, req.Sex)
		m.setPlayer(uid, p)
	} else {
		log.Infof("玩家: %d已经在线", uid)
		// 移除广播频道
		m.group.Leave(s)

		// 重置之前的session
		if prevSession := p.session; prevSession != nil && prevSession != s {
			// 如果之前房间存在，则退出来
			if p, err := playerWithSession(prevSession); err == nil && p != nil && p.desk != nil && p.desk.group != nil {
				p.desk.group.Leave(prevSession)
			}

			prevSession.Clear()
			prevSession.Close()
		}

		// 绑定新session
		p.bindSession(s)
	}

	// 添加到广播频道
	m.group.Add(s)

	res := &protocol.LoginToGameServerResponse{
		Uid:      s.UID(),
		Nickname: req.Name,
		Sex:      req.Sex,
		HeadUrl:  req.HeadUrl,
		FangKa:   req.FangKa,
	}

	return s.Response(res)
}

// 玩家返回与给定 UID 和布尔值关联的玩家
// 指示是否找到玩家。
//
// 参数：
// - uid：玩家的ID。
//
// 返回：
// - *玩家：与 UID 关联的玩家（如果找到）。
// - bool: 如果找到玩家则为 true，否则为 false。
func (m *Manager) player(uid int64) (*Player, bool) {
	p, ok := m.players[uid]

	return p, ok
}

// 设置玩家对象。如果玩家已经存在则覆盖。
func (m *Manager) setPlayer(uid int64, p *Player) {
	if _, ok := m.players[uid]; ok {
		log.Warnf("玩家已经存在，正在覆盖玩家， UID=%d", uid)
	}
	m.players[uid] = p
}

// CheckOrder 是一个在 Manager 类型上的方法，用于处理检查订单请求。
// 它接收一个会话 (s) 和一个 CheckOrderReqeust 结构 (msg) 作为参数。
// 函数内部记录消息并返回一个带有硬币数量 (FangKa) 的 CheckOrderResponse 结构。
// 如果在流程中出现错误，将返回错误。
func (m *Manager) CheckOrder(s *session.Session, msg *protocol.CheckOrderReqeust) error {
	log.Infof("%+v", msg)

	return s.Response(&protocol.CheckOrderResponse{
		FangKa: 20,
	})
}

// sessionCount 返回 Manager 的玩家集合中当前的玩家数量。
// 它计算并返回玩家map的长度。
// 此方法用于确定管理器中活动会话或玩家的数量。
func (m *Manager) sessionCount() int {
	return len(m.players)
}

// offline 从玩家集合中删除指定 uid 的玩家，并写入相应的日志消息。
// 它接收一个 uid 参数，表示要删除的玩家的唯一标识符。
// 删除操作通过调用 delete 函数和指定的 uid 作为键来完成。
// 删除后，将写入一条包含已删除玩家数量的日志消息。
// 函数没有返回值。
func (m *Manager) offline(uid int64) {
	delete(m.players, uid)
	log.Infof("玩家: %d从在线列表中删除, 剩余：%d", uid, len(m.players))
}
