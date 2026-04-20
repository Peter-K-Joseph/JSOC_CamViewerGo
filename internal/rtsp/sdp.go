package rtsp

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// BuildSDP constructs an SDP body from codec parameters.
func BuildSDP(host, streamKey, codec string, sps, pps, vps []byte) string {
	var sb strings.Builder

	sb.WriteString("v=0\r\n")
	sb.WriteString(fmt.Sprintf("o=- 0 0 IN IP4 %s\r\n", host))
	sb.WriteString("s=JSOC_CamViewerGo\r\n")
	sb.WriteString(fmt.Sprintf("i=%s\r\n", streamKey))
	sb.WriteString("t=0 0\r\n")
	sb.WriteString("a=control:*\r\n")

	switch strings.ToLower(codec) {
	case "h265", "hevc":
		sb.WriteString("m=video 0 RTP/AVP 96\r\n")
		sb.WriteString("a=rtpmap:96 H265/90000\r\n")
		if vps != nil && sps != nil && pps != nil {
			v := base64.StdEncoding.EncodeToString(vps)
			s := base64.StdEncoding.EncodeToString(sps)
			p := base64.StdEncoding.EncodeToString(pps)
			sb.WriteString(fmt.Sprintf("a=fmtp:96 sprop-vps=%s;sprop-sps=%s;sprop-pps=%s\r\n", v, s, p))
		}
	default: // h264
		sb.WriteString("m=video 0 RTP/AVP 96\r\n")
		sb.WriteString("a=rtpmap:96 H264/90000\r\n")
		if sps != nil && pps != nil {
			s := base64.StdEncoding.EncodeToString(sps)
			p := base64.StdEncoding.EncodeToString(pps)
			sb.WriteString(fmt.Sprintf("a=fmtp:96 packetization-mode=1;sprop-parameter-sets=%s,%s\r\n", s, p))
		}
	}

	sb.WriteString("a=control:trackID=0\r\n")
	sb.WriteString("a=recvonly\r\n")

	return sb.String()
}
