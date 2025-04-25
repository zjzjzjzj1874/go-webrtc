# 第一阶段：构建阶段
FROM golang:1.21-alpine AS builder

# 设置工作目录
WORKDIR /app

# 复制go.mod和go.sum文件
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 编译应用
RUN CGO_ENABLED=0 GOOS=linux go build -o webrtc-server

# 第二阶段：运行阶段
FROM alpine:latest

# 安装CA证书
RUN apk --no-cache add ca-certificates

WORKDIR /app

# 从builder阶段复制编译好的二进制文件
COPY --from=builder /app/webrtc-server .

# 复制静态文件和证书
COPY static/ ./static/
COPY cert.pem key.pem ./

# 暴露端口
EXPOSE 54321

# 运行应用
CMD ["./webrtc-server"]