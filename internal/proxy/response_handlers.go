package proxy

import (
	"io"
	"net/http"

	"autogateway/internal/pricing"
	"autogateway/internal/usage"
	"autogateway/internal/utils"

	"github.com/gin-gonic/gin"
)

// handleNormalResponse 转发非流式响应。为①成本可观测性, 这里把响应体整体
// 读入内存, 解析 token 用量后再写给客户端 —— 顺序很关键: c.Status 已在调用方
// 设置但 gin 尚未 flush 头, 故在首次 Write 之前设置 X-AC-* 头仍生效。
//
// tr 描述这次转发是否需要协议转换; 不需要时为零值, 响应原样回写。
//
// model 用于按挂牌价折算成本头; 空串或未知模型时成本为 0 (仅省略成本头,
// 仍写用量头)。非流式响应体是单个 JSON, 体量有界, 全量缓冲可接受。
func (ps *ProxyServer) handleNormalResponse(c *gin.Context, resp *http.Response, model string, tr translation) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logUpstreamError("reading response body", err)
		// 已读到的部分仍尽力回写。
	}

	// 解析用量。若响应被压缩, 解压一份副本仅用于解析; 原始 body 原样回写以保留
	// Content-Encoding。解压失败 (未知编码等) 则跳过用量解析, 不影响转发。
	parseBody := body
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		if decoded, derr := utils.DecompressResponse(enc, body); derr == nil {
			parseBody = decoded
		}
	}
	// 用量解析作用在**上游原始**响应上 —— usage.Extract 本身跨协议宽容,
	// 而且这样成本口径始终是"上游实际消耗",不受转换影响。
	if u, ok := usage.Extract(parseBody); ok {
		stashUsage(c, u)
		setUsageHeaders(c, u, pricing.Cost(model, u))
	}

	// 协议转换:把上游响应翻回客户端请求的协议形状。
	body = tr.convertResponse(body)

	if _, werr := c.Writer.Write(body); werr != nil {
		logUpstreamError("copying response body", werr)
	}
}
