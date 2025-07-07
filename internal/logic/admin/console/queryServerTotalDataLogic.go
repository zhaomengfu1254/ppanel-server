package console

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/perfect-panel/server/pkg/xerr"

	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/pkg/errors"
)

type QueryServerTotalDataLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

// NewQueryServerTotalDataLogic Query server total data
func NewQueryServerTotalDataLogic(ctx context.Context, svcCtx *svc.ServiceContext) *QueryServerTotalDataLogic {
	return &QueryServerTotalDataLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *QueryServerTotalDataLogic) QueryServerTotalData() (resp *types.ServerTotalDataResponse, err error) {

	if strings.ToLower(os.Getenv("PPANEL_MODE")) == "demo" {
		return l.mockRevenueStatistics(), nil
	}

	resp = &types.ServerTotalDataResponse{
		ServerTrafficRankingToday:     make([]types.ServerTrafficData, 0),
		ServerTrafficRankingYesterday: make([]types.ServerTrafficData, 0),
		UserTrafficRankingToday:       make([]types.UserTrafficData, 0),
		UserTrafficRankingYesterday:   make([]types.UserTrafficData, 0),
	}

	// Query node server status
	servers, err := l.svcCtx.ServerModel.FindAllServer(l.ctx)
	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] FindAllServer error", logger.Field("error", err.Error()))
		return nil, errors.Wrapf(err, "FindAllServer error: %v", err)
	}
	onlineServers, err := l.svcCtx.NodeCache.GetOnlineNodeStatusCount(l.ctx)
	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] GetOnlineNodeStatusCount error", logger.Field("error", err.Error()))
		return nil, errors.Wrapf(err, "GetOnlineNodeStatusCount error: %v", err)
	}
	resp.OnlineServers = onlineServers
	resp.OfflineServers = int64(len(servers) - int(onlineServers))

	// 获取所有节点在线用户
	allNodeOnlineUser, err := l.svcCtx.NodeCache.GetAllNodeOnlineUser(l.ctx)
	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] Get all node online user failed", logger.Field("error", err.Error()))
	}
	resp.OnlineUserIPs = int64(len(allNodeOnlineUser))

	// 获取所有节点今日上传下载流量
	allNodeUploadTraffic, err := l.svcCtx.NodeCache.GetAllNodeUploadTraffic(l.ctx)
	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] Get all node upload traffic failed", logger.Field("error", err.Error()))
	}
	resp.TodayUpload = allNodeUploadTraffic
	allNodeDownloadTraffic, err := l.svcCtx.NodeCache.GetAllNodeDownloadTraffic(l.ctx)
	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] Get all node download traffic failed", logger.Field("error", err.Error()))
	}
	resp.TodayDownload = allNodeDownloadTraffic
	// 获取节点流量排行榜 前10
	nodeTrafficRankingToday, err := l.svcCtx.NodeCache.GetNodeTodayTotalTrafficRank(l.ctx, 10)
	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] Get node today total traffic rank failed", logger.Field("error", err.Error()))
	}
	if len(nodeTrafficRankingToday) > 0 {
		var serverTrafficData []types.ServerTrafficData
		for _, rank := range nodeTrafficRankingToday {
			serverInfo, err := l.svcCtx.ServerModel.FindOne(l.ctx, rank.ID)
			if err != nil {
				l.Errorw("[QueryServerTotalDataLogic] FindOne error", logger.Field("error", err))
				continue
			}
			serverTrafficData = append(serverTrafficData, types.ServerTrafficData{
				ServerId: rank.ID,
				Name:     serverInfo.Name,
				Upload:   rank.Upload,
				Download: rank.Download,
			})
		}
		resp.ServerTrafficRankingToday = serverTrafficData
	}
	// 获取用户流量排行榜 前10
	userTrafficRankingToday, err := l.svcCtx.NodeCache.GetUserTodayTotalTrafficRank(l.ctx, 10)
	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] Get user today total traffic rank failed", logger.Field("error", err.Error()))
	}

	if len(userTrafficRankingToday) > 0 {
		var userTrafficData []types.UserTrafficData
		for _, rank := range userTrafficRankingToday {
			userTrafficData = append(userTrafficData, types.UserTrafficData{
				SID:      rank.SID,
				Upload:   rank.Upload,
				Download: rank.Download,
			})
		}
		resp.UserTrafficRankingToday = userTrafficData
	}
	// 获取昨日节点流量排行榜 前10
	nodeTrafficRankingYesterday, err := l.svcCtx.NodeCache.GetYesterdayNodeTotalTrafficRank(l.ctx)
	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] Get yesterday node total traffic rank failed", logger.Field("error", err.Error()))
	}
	if len(nodeTrafficRankingYesterday) > 0 {
		var serverTrafficData []types.ServerTrafficData
		for _, rank := range nodeTrafficRankingYesterday {
			serverTrafficData = append(serverTrafficData, types.ServerTrafficData{
				ServerId: rank.ID,
				Name:     rank.Name,
				Upload:   rank.Upload,
				Download: rank.Download,
			})
		}
		resp.ServerTrafficRankingYesterday = serverTrafficData
	}
	// 获取昨日用户流量排行榜 前10
	userTrafficRankingYesterday, err := l.svcCtx.NodeCache.GetYesterdayUserTotalTrafficRank(l.ctx)
	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] Get yesterday user total traffic rank failed", logger.Field("error", err.Error()))
	}
	if len(userTrafficRankingYesterday) > 0 {
		var userTrafficData []types.UserTrafficData
		for _, rank := range userTrafficRankingYesterday {
			userTrafficData = append(userTrafficData, types.UserTrafficData{
				SID:      rank.SID,
				Upload:   rank.Upload,
				Download: rank.Download,
			})
		}
		resp.UserTrafficRankingYesterday = userTrafficData
	}

	// Query node traffic by monthly
	nodeTraffic, err := l.svcCtx.TrafficLogModel.QueryTrafficByMonthly(l.ctx, time.Now())

	if err != nil {
		l.Errorw("[QueryServerTotalDataLogic] QueryTrafficByMonthly error", logger.Field("error", err.Error()))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "QueryTrafficByMonthly error: %v", err.Error())
	}
	resp.MonthlyUpload = nodeTraffic.Upload
	resp.MonthlyDownload = nodeTraffic.Download

	return resp, nil
}

func (l *QueryServerTotalDataLogic) mockRevenueStatistics() *types.ServerTotalDataResponse {
	now := time.Now()

	// Generate server traffic ranking data for today (top 10)
	serverTrafficToday := make([]types.ServerTrafficData, 10)
	serverNames := []string{"香港-01", "美国-洛杉矶", "日本-东京", "新加坡-01", "韩国-首尔", "台湾-01", "德国-法兰克福", "英国-伦敦", "加拿大-多伦多", "澳洲-悉尼"}
	for i := 0; i < 10; i++ {
		upload := int64(500000000 + (i * 100000000) + (i%3)*200000000)    // 500MB - 1.5GB
		download := int64(2000000000 + (i * 300000000) + (i%4)*500000000) // 2GB - 8GB
		serverTrafficToday[i] = types.ServerTrafficData{
			ServerId: int64(i + 1),
			Name:     serverNames[i],
			Upload:   upload,
			Download: download,
		}
	}

	// Generate server traffic ranking data for yesterday (top 10)
	serverTrafficYesterday := make([]types.ServerTrafficData, 10)
	for i := 0; i < 10; i++ {
		upload := int64(480000000 + (i * 95000000) + (i%3)*180000000)
		download := int64(1900000000 + (i * 280000000) + (i%4)*450000000)
		serverTrafficYesterday[i] = types.ServerTrafficData{
			ServerId: int64(i + 1),
			Name:     serverNames[i],
			Upload:   upload,
			Download: download,
		}
	}

	//// Generate user traffic ranking data for today (top 10)
	//userTrafficToday := make([]types.UserTrafficData, 10)
	//for i := 0; i < 10; i++ {
	//	upload := int64(100000000 + (i*20000000) + (i%5)*50000000)   // 100MB - 400MB
	//	download := int64(800000000 + (i*150000000) + (i%3)*300000000) // 800MB - 3GB
	//	userTrafficToday[i] = types.UserTrafficData{
	//		SID:      int64(10001 + i),
	//		Upload:   upload,
	//		Download: download,
	//	}
	//}

	//// Generate user traffic ranking data for yesterday (top 10)
	//userTrafficYesterday := make([]types.UserTrafficData, 10)
	//for i := 0; i < 10; i++ {
	//	upload := int64(95000000 + (i*18000000) + (i%5)*45000000)
	//	download := int64(750000000 + (i*140000000) + (i%3)*280000000)
	//	userTrafficYesterday[i] = types.UserTrafficData{
	//		SID:      int64(10001 + i),
	//		Upload:   upload,
	//		Download: download,
	//	}
	//}
	//
	return &types.ServerTotalDataResponse{
		OnlineUserIPs:                 1688,
		OnlineServers:                 8,
		OfflineServers:                2,
		TodayUpload:                   8888888888,   // ~8.3GB
		TodayDownload:                 28888888888,  // ~26.9GB
		MonthlyUpload:                 288888888888, // ~269GB
		MonthlyDownload:               888888888888, // ~828GB
		UpdatedAt:                     now.Unix(),
		ServerTrafficRankingToday:     serverTrafficToday,
		ServerTrafficRankingYesterday: serverTrafficYesterday,
		//UserTrafficRankingToday:       userTrafficToday,
		//UserTrafficRankingYesterday:   userTrafficYesterday,
	}
}
