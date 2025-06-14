# Docker多阶段构建Golang应用代码分析

这是一个使用多阶段构建(multi-stage build)的Dockerfile，用于构建和部署一个名为"ppanel"的Go应用程序。多阶段构建的主要优点是可以显著减小最终镜像的大小。

## 第一阶段：构建阶段

```dockerfile
FROM golang:alpine AS builder
LABEL stage=gobuilder
ARG TARGETARCH
ARG VERSION
ENV CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH}
```
- 使用轻量级的`golang:alpine`作为基础镜像
- 将该阶段标记为`builder`，方便后续引用
- 定义构建参数`TARGETARCH`(目标架构)和`VERSION`(版本号)
- 设置Go编译环境变量：禁用CGO，指定为Linux系统，架构使用传入的参数

```dockerfile
RUN apk update --no-cache && apk add --no-cache tzdata ca-certificates
WORKDIR /build
```
- 安装时区数据和CA证书
- 设置工作目录为`/build`

```dockerfile
COPY go.mod go.sum ./
RUN go mod download
COPY . .
```
- 先复制Go的依赖文件并下载依赖（利用Docker的缓存机制，如果依赖没变，这一步可以复用缓存）
- 然后再复制所有源代码

```dockerfile
RUN BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ") && \
    go build -ldflags="-s -w -X 'github.com/perfect-panel/server/pkg/constant.Version=${VERSION}' -X 'github.com/perfect-panel/server/pkg/constant.BuildTime=${BUILD_TIME}'" -o /app/ppanel ppanel.go
```
- 获取当前UTC时间作为构建时间
- 使用`go build`命令编译应用，主要特点：
    - `-ldflags="-s -w"` 减小二进制文件大小（去除调试信息）
    - 注入版本号和构建时间到应用程序中
    - 输出编译后的二进制文件到`/app/ppanel`

## 第二阶段：运行阶段

```dockerfile
FROM scratch
```
- 使用`scratch`作为基础镜像，这是一个空镜像，进一步减小最终镜像的大小

```dockerfile
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo/Asia/Shanghai /usr/share/zoneinfo/Asia/Shanghai
ENV TZ=Asia/Shanghai
```
- 从构建阶段复制必要的SSL证书和上海时区数据
- 设置容器时区为上海

```dockerfile
WORKDIR /app
COPY --from=builder /app/ppanel /app/ppanel
COPY --from=builder /build/etc /app/etc
```
- 设置工作目录为`/app`
- 从构建阶段复制编译好的二进制文件和配置文件目录

```dockerfile
EXPOSE 8080
ENTRYPOINT ["/app/ppanel"]
CMD ["run", "--config", "etc/ppanel.yaml"]
```
- 声明容器将使用8080端口（仅作为文档说明，实际上需要在运行时映射）
- 设置容器启动时执行的命令：运行ppanel应用并指定配置文件路径

## 总结

这是一个遵循Docker最佳实践的Dockerfile：
1. 使用多阶段构建减小镜像大小
2. 利用缓存机制优化构建速度
3. 使用`scratch`作为最终基础镜像，最小化攻击面
4. 正确处理SSL证书和时区设置
5. 清晰指定入口点和默认命令