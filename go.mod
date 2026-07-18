module github.com/chnu-kim/toss-trade-bot

go 1.25.0

// 무인 봇: 빌드에 쓰이는 stdlib 패치 버전을 환경 툴체인에 맡기지 않고 고정한다.
// go1.26.0의 도달 가능한 stdlib 취약점(malformed 인증서 panic 등 — 크래시=주문 루프 정지)이
// 이후 패치에서 수정됐다. 이 floor는 govulncheck ./...가 도달 가능 stdlib 취약점 0건이 되는
// 최신 패치로 맞춘다. CI의 govulncheck 스텝이 이 floor의 패치 드리프트를 자동 감지한다.
toolchain go1.26.5

require modernc.org/sqlite v1.53.0

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.44.0 // indirect
	modernc.org/libc v1.73.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
