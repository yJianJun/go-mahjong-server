package game

import (
	"go-mahjong-server/protocol"

	"go-mahjong-server/db"
	"go-mahjong-server/pkg/async"

	"github.com/lonng/nano/component"
	"github.com/lonng/nano/session"
)

// ClubManager 代表俱乐部相关操作的管理者。
type ClubManager struct {
	component.Base
}

// ApplyClub 为玩家申请俱乐部会员资格。
//
// 它需要一个会话和一个ApplyClubRequest负载作为参数。
// 会话包含有关玩家的信息，例如用户 ID (UID)。
// 负载中包含玩家想要加入的俱乐部的 ID (ClubId)。
//
// 该方法使用记录器以调试级别记录玩家的请求。
// 然后它使用 async.Run 创建一个新的 goroutine 来异步处理应用程序。
// 在goroutine中，调用db.ApplyClub方法来处理申请。
// 如果处理过程中出现错误，则会向播放器发送一个包含错误消息的 ErrorResponse。
// 否则，SuccessResponse 会被发送回玩家。
//
// ApplyClub 不会返回任何错误。
func (c *ClubManager) ApplyClub(s *session.Session, payload *protocol.ApplyClubRequest) error {
	mid := s.LastMid()
	logger.Debugf("玩家申请加入俱乐部，UID=%d，俱乐部ID=%d", s.UID(), payload.ClubId)
	async.Run(func() {
		if err := db.ApplyClub(s.UID(), payload.ClubId); err != nil {
			s.ResponseMID(mid, &protocol.ErrorResponse{
				Code:  -1,
				Error: err.Error(),
			})
		} else {
			s.ResponseMID(mid, &protocol.SuccessResponse)
		}
	})
	return nil
}
