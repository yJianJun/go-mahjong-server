package game

import (
	"fmt"
	"strings"
	"time"

	"go-mahjong-server/db"
	"go-mahjong-server/pkg/async"
	"go-mahjong-server/pkg/constant"
	"go-mahjong-server/pkg/errutil"
	"go-mahjong-server/pkg/room"
	"go-mahjong-server/protocol"

	"github.com/lonng/nano/scheduler"

	"github.com/lonng/nano/component"
	"github.com/lonng/nano/session"
	"github.com/pkg/errors"
)

const (
	ApplyDissolve = "申请解散"
	AgreeRequest  = "同意解散"
	Offline       = "离线"
	Waiting       = "等待中"

	fieldDesk   = "desk"
	fieldPlayer = "player"
)

const deskOpBacklog = 64

const (
	errorCode             = -1  //错误码
	applyDissolveRestTime = 300 //有玩家申请解散, 倒计时5分钟
	agreeDissolveRestTime = 150 //如果有一个玩家同意解散, 时间降低为2.5分钟
)

const (
	deskNotFoundMessage        = "您输入的房间号不存在, 请确认后再次输入"
	deskPlayerNumEnoughMessage = "您加入的房间已经满人, 请确认房间号后再次确认"
	versionExpireMessage       = "你当前的游戏版本过老，请更新客户端，地址: http://fir.im/tand"
	deskCardNotEnoughMessage   = "房卡不足"
	clubCardNotEnoughMessage   = "俱乐部房卡不足"
)

var ErrModeCannotQue = errors.New("当前不为4人模式，不能定缺")

var (
	deskNotFoundResponse = &protocol.JoinDeskResponse{Code: errutil.YXDeskNotFound, Error: deskNotFoundMessage}
	deskPlayerNumEnough  = &protocol.JoinDeskResponse{Code: errorCode, Error: deskPlayerNumEnoughMessage}
	joinVersionExpire    = &protocol.JoinDeskResponse{Code: errorCode, Error: versionExpireMessage}
	reentryDesk          = &protocol.CreateDeskResponse{Code: 30003, Error: "你当前正在房间中"}
	createVersionExpire  = &protocol.CreateDeskResponse{Code: 30001, Error: versionExpireMessage}
	deskCardNotEnough    = &protocol.CreateDeskResponse{Code: 30002, Error: deskCardNotEnoughMessage}
	clubCardNotEnough    = &protocol.CreateDeskResponse{Code: 30002, Error: clubCardNotEnoughMessage}
)

// DeskManager负责管理系统中所有的牌桌
type (
	DeskManager struct {
		component.Base
		//桌子数据
		desks map[room.Number]*Desk // 所有桌子
	}
)

var defaultDeskManager = NewDeskManager()

func NewDeskManager() *DeskManager {
	return &DeskManager{
		desks: map[room.Number]*Desk{},
	}
}

// AfterInit 是 DeskManager 初始化后调用的方法。
// 它向会话的生命周期对象注册一个“OnClosed”回调来处理玩家断开连接。
// 如果会话的 UID 大于零，则调用 DeskManager 的“onPlayerDisconnect”方法，
// 并记录错误（如果有）。
// 此外，它还安排一个计时器每 5 分钟运行一次，这会清除被破坏的房间信息，
// 销毁超过 24 小时不活动的房间，并转储更新的办公桌信息。
// 最后，将在线会话数和办公桌数异步插入数据库。
func (manager *DeskManager) AfterInit() {
	session.Lifetime.OnClosed(func(s *session.Session) {
		// Fixed: 玩家WIFI切换到4G网络不断开, 重连时，将UID设置为illegalSessionUid
		if s.UID() > 0 {
			if err := manager.onPlayerDisconnect(s); err != nil {
				logger.Errorf("玩家退出: UID=%d, Error=%s", s.UID, err.Error())
			}
		}
	})

	// 每5分钟清空一次已摧毁的房间信息
	scheduler.NewTimer(300*time.Second, func() {
		destroyDesk := map[room.Number]*Desk{}
		deadline := time.Now().Add(-24 * time.Hour).Unix()
		for no, d := range manager.desks {
			// 清除创建超过24小时的房间
			if d.status() == constant.DeskStatusDestory || d.createdAt < deadline {
				destroyDesk[no] = d
			}
		}
		for _, d := range destroyDesk {
			d.destroy()
		}

		manager.dumpDeskInfo()

		// 统计结果异步写入数据库
		sCount := defaultManager.sessionCount()
		dCount := len(manager.desks)
		async.Run(func() {
			db.InsertOnline(sCount, dCount)
		})
	})
}

// dumpDeskInfo 打印剩余房间数量和在线人数，以及每个房间的详细信息。
// 如果没有房间，则直接返回。
// 输出信息包括房间号、创建时间、创建玩家、状态、总局数和当前局数。
func (manager *DeskManager) dumpDeskInfo() {
	c := len(manager.desks)
	if c < 1 {
		return
	}

	logger.Infof("剩余房间数量: %d 在线人数: %d  当前时间: %s", c, defaultManager.sessionCount(), time.Now().Format("2006-01-02 15:04:05"))
	for no, d := range manager.desks {
		logger.Debugf("房号: %s, 创建时间: %s, 创建玩家: %d, 状态: %s, 总局数: %d, 当前局数: %d",
			no, time.Unix(d.createdAt, 0).String(), d.creator, d.status().String(), d.opts.MaxRound, d.round)
	}
}

// onPlayerDisconnect 是 DeskManager 的方法，用于处理玩家断开连接的情况。
// 它首先获取会话的 UID，并通过 playerWithSession 函数获取关联的玩家对象。
// 如果获取玩家对象的过程中发生错误，则返回该错误。
// 否则，记录调试日志，并移除会话。
// 如果玩家没有加入任何房间或者所在房间已被销毁，则调用 defaultManager 的 offline 方法。
// 否则，调用玩家所在房间的 onPlayerExit 方法，并传入 true 参数表示玩家断开连接。
// 最后，返回 nil 表示没有错误发生。
func (manager *DeskManager) onPlayerDisconnect(s *session.Session) error {
	uid := s.UID()
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}
	p.logger.Debug("DeskManager.onPlayerDisconnect: 玩家网络断开")

	// 移除session
	p.removeSession()

	if p.desk == nil || p.desk.isDestroy() {
		defaultManager.offline(uid)
		return nil
	}

	d := p.desk
	d.onPlayerExit(s, true)
	return nil
}

// desk 方法根据桌号返回桌子对象和是否存在的布尔值。
func (manager *DeskManager) desk(number room.Number) (*Desk, bool) {
	d, ok := manager.desks[number]
	return d, ok
}

// setDesk 设置桌号对应的牌桌数据
func (manager *DeskManager) setDesk(number room.Number, desk *Desk) {
	if desk == nil {
		delete(manager.desks, number)
		logger.WithField(fieldDesk, number).Debugf("清除房间: 剩余: %d", len(manager.desks))
	} else {
		manager.desks[number] = desk
	}
}

// UnCompleteDesk 检查登录玩家关闭应用之前是否正在游戏。
// 如果玩家没有加入房间，则返回一个空的 UnCompleteDeskResponse。
// 否则，检查房间是否已销毁，如果已销毁，则从 DeskManager 中删除该房间，并将玩家的 desk 字段设置为 nil。
// 如果房间未销毁，则返回包含房间信息的非空 UnCompleteDeskResponse 对象。
// UnCompleteDeskResponse 结构包含一个布尔值 Exist 和一个 TableInfo 结构。
// Exist 表示房间是否存在，TableInfo 包含房间的详细信息。
func (manager *DeskManager) UnCompleteDesk(s *session.Session, _ []byte) error {
	resp := &protocol.UnCompleteDeskResponse{}

	p, err := playerWithSession(s)
	if err != nil {
		return nil
	}

	if p.desk == nil {
		p.logger.Debug("DeskManager.UnCompleteDesk: 玩家不在房间内")
		return s.Response(resp)
	}

	d := p.desk
	if d.isDestroy() {
		delete(manager.desks, d.roomNo)
		p.desk = nil
		p.logger.Debug("DeskManager.UnCompleteDesk: 房间已销毁")
		return s.Response(resp)
	}

	return s.Response(&protocol.UnCompleteDeskResponse{
		Exist: true,
		TableInfo: protocol.TableInfo{
			DeskNo:    string(d.roomNo),
			CreatedAt: d.createdAt,
			Creator:   d.creator,
			Title:     d.title(),
			Desc:      d.desc(true),
			Status:    d.status(),
			Round:     d.round,
			Mode:      d.opts.Mode,
		},
	})
}

// ReConnect 处理网络断开后重新连接网络的逻辑。
// 它接收一个会话 (s) 和一个 ReConnect 结构 (req) 作为参数。
// 函数首先绑定 UID 到会话。
// 如果绑定出错，则返回错误。
// 函数记录重新连接服务器的日志消息，并根据之前的 UID 查找玩家。
// 如果找不到玩家，则说明该玩家之前的用户信息已被清除，
// 创建一个新的玩家，并存储到玩家集合中。
// 如果找到玩家，则替换之前的会话，并解绑之前的会话。
// 如果之前的会话存在，则将其清除和关闭，并绑定新的会话。
// 如果玩家当前在某个桌子上，则将其从广播组中移除。
// 最后，函数返回 nil。
func (manager *DeskManager) ReConnect(s *session.Session, req *protocol.ReConnect) error {
	uid := req.Uid

	// 绑定UID
	if err := s.Bind(uid); err != nil {
		return err
	}

	logger.Infof("玩家重新连接服务器: UID=%d", uid)

	// 设置用户
	p, ok := defaultManager.player(uid)
	if !ok {
		logger.Infof("玩家之前用户信息已被清除，重新初始化用户信息: UID=%d", uid)
		ip := ""
		if parts := strings.Split(s.RemoteAddr().String(), ":"); len(parts) > 0 {
			ip = parts[0]
		}
		p = newPlayer(s, uid, req.Name, req.HeadUrl, ip, req.Sex)
		defaultManager.setPlayer(uid, p)
	} else {
		logger.Infof("玩家之前用户信息存在服务器上，替换session: UID=%d", uid)

		// 重置之前的session
		prevSession := p.session
		if prevSession != nil {
			prevSession.Clear()
			prevSession.Close()
		}

		// 绑定新session
		p.bindSession(s)

		// 移除广播频道
		if d := p.desk; d != nil && prevSession != nil {
			d.group.Leave(prevSession)
		}
	}

	return nil
}

// ReJoin 是一个DeskManager的方法，用于在网络断开后重新加入房间。
// 如果重新连接后发现当前在房间中，则使用之前的桌号重新进入房间。
// 首先，使用房间号查找房间是否存在，如果不存在或者已解散，则返回错误信息。
// 如果找到了未解散的房间，记录调试信息，表示玩家重新加入房间。
// 最后，调用房间的“onPlayerReJoin”方法处理玩家重新加入的逻辑。
func (manager *DeskManager) ReJoin(s *session.Session, data *protocol.ReJoinDeskRequest) error {
	d, ok := manager.desk(room.Number(data.DeskNo))
	if !ok || d.isDestroy() {
		return s.Response(&protocol.ReJoinDeskResponse{
			Code:  -1,
			Error: "房间已解散",
		})
	}
	d.logger.Debugf("玩家重新加入房间: UID=%d, Data=%+v", s.UID(), data)

	return d.onPlayerReJoin(s)
}

// ReEnter 是 DeskManager 的一个方法，用于在应用退出后重新进入房间。
// 它接收一个会话对象和一个 ReEnterDeskRequest 对象作为参数。
// 如果通过会话获取的玩家对象出现错误，则记录错误并返回。
// 如果玩家没有未完成的房间，但发送了重进请求，则记录警告并返回。
// 如果玩家试图进入的房间与上次未完成的房间不匹配，则记录警告并返回。
// 否则，调用房间的 onPlayerReJoin 方法，并返回结果。
func (manager *DeskManager) ReEnter(s *session.Session, msg *protocol.ReEnterDeskRequest) error {
	p, err := playerWithSession(s)
	if err != nil {
		logger.Errorf("玩家重新进入房间: UID=%d", s.UID())
		return nil
	}

	if p.desk == nil {
		p.logger.Debugf("玩家没有未完成房间，但是发送了重进请求: 请求房号: %s", msg.DeskNo)
		return nil
	}

	d := p.desk

	if string(d.roomNo) != msg.DeskNo {
		p.logger.Debugf("玩家正在试图进入非上次未完成房间: 房号: %s", d.roomNo)
		return nil
	}

	return d.onPlayerReJoin(s)
}

// Pause 是 DeskManager 的方法，用于将玩家设置为离线状态
// 它接受一个会话对象和一个无用的字节参数，返回一个错误对象
// 如果会话中找不到与之关联的玩家，则返回一个错误
// 如果玩家在房间内，将其设置为离线状态
// 否则，记录一条调试级别的日志，并返回 nil
func (manager *DeskManager) Pause(s *session.Session, _ []byte) error {
	uid := s.UID()
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	d := p.desk
	if d == nil {
		p.logger.Debug("玩家不在房间内")
		return nil
	}

	p.logger.Debug("玩家切换到后台")
	d.dissolve.updateOnlineStatus(uid, false)

	return nil
}

// Resume 是 DeskManager 的方法，用于恢复玩家切换到前台的操作。
// 它接收一个会话和一个空的字节参数，并返回一个错误。
// 首先，它获取会话的 UID，并使用 playerWithSession 函数获取与会话关联的玩家。
// 如果出现错误，将返回该错误。
// 然后，它获取玩家所在的房间，并检查玩家是否已经在房间中。
// 如果玩家不在房间中，则记录日志并返回。
// 接下来，检查是否存在解散操作，并且玩家没有在线。
// 如果是这样，则更新玩家的在线状态，并返回。
// 最后，检查房间玩家人数是否达到所需人数，是否已经有人申请解散。
// 如果是这样，向房间内的玩家广播最新的解散状态，并返回。
func (manager *DeskManager) Resume(s *session.Session, _ []byte) error {
	uid := s.UID()
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	d := p.desk
	if d == nil {
		p.logger.Debug("玩家不在房间内")
		return nil
	}

	// 玩家并未暂停
	if d.dissolve.isOnline(uid) {
		return nil
	}

	p.logger.Debug("玩家切换到前台")
	d.dissolve.updateOnlineStatus(uid, true)

	// 人数不够, 未开局, 或没有人申请解散
	if len(d.players) < d.totalPlayerCount() || !d.dissolve.isDissolving() {
		return nil
	}

	// 有玩家切出游戏, 切回来时发现已经有人申请解散, 则刷新最新的解散状态
	p.logger.Debug("已经有人申请退出了")
	dissolveStatus := &protocol.DissolveStatusResponse{
		DissolveStatus: d.collectDissolveStatus(),
		RestTime:       d.dissolve.restTime,
	}

	return d.group.Broadcast("onDissolveStatus", dissolveStatus)
}

// QiPaiFinished 是 DeskManager 的方法，用于处理理牌结束操作。
// 它接收一个会话对象和一段消息作为参数。
// 首先，通过会话对象获取玩家对象，如果获取失败则返回错误。
// 接下来，获取当前玩家所在的房间。
// 如果玩家不在房间内，则记录调试日志并返回nil。
// 最后，调用房间对象的qiPaiFinished方法，并传入玩家的UID作为参数。
// 如果发生错误，将会被返回。
func (manager *DeskManager) QiPaiFinished(s *session.Session, msg []byte) error {
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	d := p.desk
	if d == nil {
		p.logger.Debug("玩家不在房间内")
		return nil
	}

	return d.qiPaiFinished(s.UID())
}

// DingQue 是 DeskManager 的方法，用于玩家定缺麻将。
// 首先，根据会话获取对应的玩家对象。
// 值得注意的是，如果会话中不存在玩家对象，则会返回错误。
// 然后，根据传入的定缺参数进行验证，如果参数小于1，则返回相应的错误。
// 接着，获取玩家所在的房间对象。
// 如果玩家不在任何房间中，则打印日志并直接返回。
// 如果房间的模式不是4人模式，则返回模式不支持定缺的错误。
// 最后，调用房间的 dingQue 方法，将定缺信息应用于玩家。
// 函数最终返回 nil，表示没有错误发生。
func (manager *DeskManager) DingQue(s *session.Session, msg *protocol.DingQue) error {
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	que := msg.Que
	if que < 1 {
		return fmt.Errorf("玩家定缺麻将不能为0，实际=%d", que)
	}

	d := p.desk
	if d == nil {
		p.logger.Debug("玩家不在房间内")
		return nil
	}

	if d.opts.Mode != ModeFours {
		return ErrModeCannotQue
	}

	d.dingQue(p, que)
	return nil
}

// Exit 处理玩家退出, 客户端会在房间人没有满的情况下发送DeskManager.Exit消息, 如果人满, 或游戏
// 开始, 客户端则发送DeskManager.Dissolve申请解散
func (manager *DeskManager) Exit(s *session.Session, msg *protocol.ExitRequest) error {
	uid := s.UID()
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}
	p.logger.Debugf("DeskManager.Exit: %+v", msg)
	d := p.desk
	if d == nil || d.isDestroy() {
		p.logger.Debug("玩家不在房间内")
		return s.Push("onDissolveSuccess", protocol.EmptyMessage)
	}

	if d.status() != constant.DeskStatusCreate {
		p.logger.Debug("房间已经开始，中途不能退出")
		return nil
	}

	deskPos := -1
	for i, p := range d.players {
		if p.Uid() == uid {
			deskPos = i
			if !d.prepare.isReady(uid) {
				// fixed: 玩家在未准备的状态退出游戏, 不应该直接返回
				msg := &protocol.ExitResponse{
					AccountId: uid,
					IsExit:    true,
					ExitType:  protocol.ExitTypeExitDeskUI,
					DeskPos:   deskPos,
				}
				if err := s.Push("onDissolve", msg); err != nil {
					return err
				}
			}
			break
		}
	}

	res := &protocol.ExitResponse{
		AccountId: uid,
		IsExit:    true,
		ExitType:  protocol.ExitTypeExitDeskUI,
		DeskPos:   deskPos,
	}
	route := "onPlayerExit"
	if msg.IsDestroy {
		route = "onDissolve"
	}
	d.group.Broadcast(route, res)

	p.logger.Info("DeskManager.Exit: 退出房间")
	d.onPlayerExit(s, false)

	return nil
}

// OpChoose 方法处理玩家选择操作。
// 它根据会话获取相应的玩家数据，如果获取失败则返回错误。
// 该方法将收到的 OpChooseRequest 消息记录到玩家的日志中，
// 并将操作消息通过玩家的操作通道发送出去。
// 返回 nil 表示处理成功。
func (manager *DeskManager) OpChoose(s *session.Session, msg *protocol.OpChooseRequest) error {
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	p.logger.Debugf("玩家选择: MSG=%+v", msg)
	p.chOperation <- &protocol.OpChoosed{
		Type:   msg.OpType,
		TileID: msg.Index,
	}
	return nil
}

// Ready 是 DeskManager 的 Ready 方法。
// 该方法用于准备玩家，将玩家与会话关联，并执行一系列操作。
// 首先，使用 playerWithSession 方法将会话与玩家进行关联。
// 如果获取玩家失败，则返回错误。
// 接着，获取玩家对应的桌子，并进行准备操作。
// 然后，同步桌子状态。
// 在广播消息之后必须调用 checkStart 方法。
// 最后，返回可能出现的错误。
func (manager *DeskManager) Ready(s *session.Session, _ []byte) error {
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	d := p.desk
	d.prepare.ready(s.UID())
	d.syncDeskStatus()

	// 必须在广播消息以后调用checkStart
	d.checkStart()
	return err
}

func (manager *DeskManager) ClientInitCompleted(s *session.Session, msg *protocol.ClientInitCompletedRequest) error {
	logger.Debug(msg)
	uid := s.UID()
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	d := p.desk
	// 客户端准备完成后加入消息广播队列
	for _, p := range d.players {
		if p.Uid() == uid {
			if p.session != s {
				p.logger.Error("DeskManager.ClientInitCompleted: Session不一致")
			}
			p.logger.Info("eskManager.ClientInitCompleted: 玩家加入房间广播列表")
			d.group.Add(p.session)
			break
		}
	}

	// 如果不是重新进入游戏, 则同步状态到房间所有玩家
	if !msg.IsReEnter {
		d.syncDeskStatus()
	}

	return err
}

// 创建一张桌子
func (manager *DeskManager) CreateDesk(s *session.Session, data *protocol.CreateDeskRequest) error {
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	if p.desk != nil {
		return s.Response(reentryDesk)
	}
	if forceUpdate && data.Version != version {
		return s.Response(createVersionExpire)
	}

	logger.Infof("牌桌选项: %#v", data.DeskOpts)

	if !verifyOptions(data.DeskOpts) {
		return errutil.ErrIllegalParameter
	}

	// 四人模式，默认可以平胡
	if data.DeskOpts.Mode == ModeFours {
		data.DeskOpts.Pinghu = true
	}

	// TODO: 测试只打一轮
	//data.DeskOpts.MaxRound = 1

	// 非俱乐部模式房卡数判定
	if data.ClubId < 0 {
		count := requireCardCount(data.DeskOpts.MaxRound)
		if p.coin < int64(count) {
			return s.Response(deskCardNotEnough)
		}

	} else {
		if db.IsBalanceEnough(data.ClubId) == false {
			return s.Response(clubCardNotEnough)
		}
	}

	no := room.Next()
	d := NewDesk(no, data.DeskOpts, data.ClubId)
	d.createdAt = time.Now().Unix()
	d.creator = s.UID()
	//房间创建者自动join
	if err := d.playerJoin(s, false); err != nil {
		return nil
	}

	// save desk information
	manager.desks[no] = d

	resp := &protocol.CreateDeskResponse{
		TableInfo: protocol.TableInfo{
			DeskNo:    string(no),
			CreatedAt: d.createdAt,
			Creator:   s.UID(),
			Title:     d.title(),
			Desc:      d.desc(true),
			Status:    d.status(),
			Round:     d.round,
			Mode:      d.opts.Mode,
		},
	}
	d.logger.Infof("当前已有牌桌数: %d", len(manager.desks))
	return s.Response(resp)
}

// 新join在session的context中尚未有desk的cache
func (manager *DeskManager) Join(s *session.Session, data *protocol.JoinDeskRequest) error {
	if forceUpdate && data.Version != version {
		return s.Response(joinVersionExpire)
	}

	dn := room.Number(data.DeskNo)
	d, ok := manager.desk(dn)
	if !ok {
		return s.Response(deskNotFoundResponse)
	}

	if len(d.players) >= d.totalPlayerCount() {
		return s.Response(deskPlayerNumEnough)
	}

	// 如果是俱乐部房间，则判断玩家是否是俱乐部玩家
	// 否则直接加入房间
	if d.clubId > 0 {
		if db.IsClubMember(d.clubId, s.UID()) == false {
			return s.Response(&protocol.JoinDeskResponse{
				Code:  errorCode,
				Error: fmt.Sprintf("当前房间是俱乐部[%d]专属房间，俱乐部成员才可加入", d.clubId),
			})
		}
	}

	if err := d.playerJoin(s, false); err != nil {
		d.logger.Errorf("玩家加入房间失败，UID=%d, Error=%s", s.UID(), err.Error())
	}

	return s.Response(&protocol.JoinDeskResponse{
		TableInfo: protocol.TableInfo{
			DeskNo:    d.roomNo.String(),
			CreatedAt: d.createdAt,
			Creator:   d.creator,
			Title:     d.title(),
			Desc:      d.desc(true),
			Status:    d.status(),
			Round:     d.round,
			Mode:      d.opts.Mode,
		},
	})
}

// 有玩家请求解散房间
func (manager *DeskManager) Dissolve(s *session.Session, msg []byte) error {
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	d := p.desk
	if d == nil || d.isDestroy() {
		logger.Infof("玩家: %d申请解散，但是房间为空或者已解散", s.UID())
		return s.Push("onDissolveSuccess", protocol.EmptyMessage)
	}

	d.applyDissolve(s.UID())

	return nil
}

// 玩家同意或拒绝解散房间请求
func (manager *DeskManager) DissolveStatus(s *session.Session, data *protocol.DissolveStatusRequest) error {
	logger.Debugf("DeskManager.DissolveStatus: %+v", data)
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	d := p.desk
	if d == nil || d.isDestroy() {
		p.logger.Infof("申请解散，但是房间为空或者已解散")
		return s.Push("onDissolveSuccess", protocol.EmptyMessage)
	}

	// 有玩家拒绝，则清空解散统计数据
	if !data.Result {
		deskPos := -1
		for i, p := range d.players {
			if p.Uid() == s.UID() {
				deskPos = i + 1
				break
			}
		}

		d.dissolve.reset()
		d.dissolve.stop()
		return d.group.Broadcast("onDissolveFailure", &protocol.DissolveResult{DeskPos: deskPos})
	} else {
		d.dissolve.setUidStatus(s.UID(), true, AgreeRequest)
		if d.dissolve.restTime > agreeDissolveRestTime {
			d.dissolve.restTime = agreeDissolveRestTime
		}
		status := &protocol.DissolveStatusResponse{
			DissolveStatus: d.collectDissolveStatus(),
			RestTime:       d.dissolve.restTime,
		}
		if err := d.group.Broadcast("onDissolveStatus", status); err != nil {
			logger.Error(err)
		}

		if d.dissolve.agreeCount() < d.totalPlayerCount() {
			return nil
		}

		d.logger.Debug("所有玩家同意解散, 即将解散")

		d.dissolve.stop()
		d.doDissolve()
	}
	return nil
}

// 玩家语音消息
func (manager *DeskManager) VoiceMessage(s *session.Session, msg []byte) error {
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	d := p.desk
	if d != nil && d.group != nil {
		return d.group.Broadcast("onVoiceMessage", msg)
	}

	return nil
}

// 玩家录制完语音
func (manager *DeskManager) RecordingVoice(s *session.Session, msg *protocol.RecordingVoice) error {
	p, err := playerWithSession(s)
	if err != nil {
		return err
	}

	d := p.desk
	resp := &protocol.PlayRecordingVoice{
		Uid:    s.UID(),
		FileId: msg.FileId,
	}

	if d != nil && d.group != nil {
		return d.group.Broadcast("onRecordingVoice", resp)
	}
	return nil
}
