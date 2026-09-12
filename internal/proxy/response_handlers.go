package proxy

import (
	"fmt"
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

	// 若响应被压缩, 解压一份副本。原始 body 保留用于回写(不转译时), 副本用于
	// 解析用量 —— 以及转译(JSON 解析器看不懂 gzip)。
	// decoded==true 表示 body 确实是明文(上游未压缩, 或压缩已成功解开)。
	parseBody, decoded := body, true
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		decoded = false
		if plain, derr := utils.DecompressResponse(enc, body); derr == nil {
			parseBody, decoded = plain, true
		}
	}

	// 用量解析作用在**上游原始**响应上 —— usage.Extract 本身跨协议宽容,
	// 而且这样成本口径始终是"上游实际消耗",不受转换影响。
	if u, ok := usage.Extract(parseBody); ok {
		stashUsage(c, u)
		setUsageHeaders(c, u, pricing.Cost(model, u))
	}

	// 协议转换:把上游响应翻回客户端请求的协议形状。
	//
	// 转译必须在**明文**上做。调用方在 tr.needed 时已经丢掉了上游的
	// Content-Length / Content-Encoding, 所以这里写出去的必须是明文 —— 若
	// 压缩体解不开, 就只能放弃转译并把编码头补回去, 否则客户端会拿到一段
	// 没有 Content-Encoding 的压缩字节, 直接乱码。
	if tr.needed {
		if decoded {
			body = tr.convertResponse(parseBody)
		} else {
			c.Header("Content-Encoding", resp.Header.Get("Content-Encoding"))
			logUpstreamError("translation skipped: cannot decode upstream response body",
				fmt.Errorf("unsupported or malformed encoding %q", resp.Header.Get("Content-Encoding")))
		}
	}

	if _, werr := c.Writer.Write(body); werr != nil {
		logUpstreamError("copying response body", werr)
	}
}
