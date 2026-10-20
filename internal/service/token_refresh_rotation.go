// Package service — refresh token 轮换的并发保护与幂等复用。
//
// 背景：refresh token 是一次性凭证，刷新成功后旧 token 立即作废。但真实
// 客户端天然会并发使用同一个令牌：
//
//   - 同一个标签页里，WebSocket 重连与 axios 401 拦截器会各发一次刷新；
//   - 多个标签页共享同一份 localStorage 里的 refresh token；
//   - 容器重启/部署后，多个页面同时重新加载并刷新。
//
// 此前第二个请求必然收到 401（revoked），前端把 401 当成会话彻底失效，
// 清掉本地令牌并跳登录页——外部表现就是「每次部署之后都要重新登录」。
//
// 本文件提供两层保护：
//
//  1. refreshFlights：同一个 token 的并发刷新共享同一次轮换结果；
//  2. rotations：轮换后的宽限期内重复提交同一个旧 token，返回同一次轮换
//     产生的令牌对（幂等），超过宽限期仍按已撤销处理。
package service

import (
	"strings"
	"time"
)

// refreshTokenReuseGrace 是 refresh token 轮换后的幂等复用宽限期。
//
// 取值只需覆盖「并发请求 + 客户端一次重试」的量级，越短越好：宽限期内
// 持有旧 token 的一方仍能换取同一个会话，超过则按 token 复用检测处理。
const refreshTokenReuseGrace = 60 * time.Second

// rotatedRefreshToken 保存一次轮换产生的令牌对，供宽限期内幂等复用。
//
// 这里必须保留 refresh token 明文——客户端要用它继续会话，而哈希无法反推。
// 条目只存在于内存、随进程结束消失，并在宽限期结束后回收，不落库。
type rotatedRefreshToken struct {
	userID    string
	pair      *TokenPair
	expiresAt time.Time
}

// refreshFlight 是一次进行中的刷新，供并发请求共享结果。
type refreshFlight struct {
	done chan struct{}
	pair *TokenPair
	err  error
}

// currentTime 返回当前时间；测试可注入固定时间源。
func (s *TokenService) currentTime() time.Time {
	if s == nil || s.now == nil {
		return time.Now()
	}
	return s.now()
}

// startRefreshFlight 尝试成为某个 refresh token 的首个刷新者。
// 返回 (flight, true) 表示调用方负责执行刷新并调用 finishRefreshFlight；
// 返回 (flight, false) 表示已有刷新在途，需要等待它的结果。
func (s *TokenService) startRefreshFlight(tokenHash string) (*refreshFlight, bool) {
	s.rotateMu.Lock()
	defer s.rotateMu.Unlock()
	if s.refreshFlights == nil {
		s.refreshFlights = make(map[string]*refreshFlight)
	}
	if existing, ok := s.refreshFlights[tokenHash]; ok {
		return existing, false
	}
	flight := &refreshFlight{done: make(chan struct{})}
	s.refreshFlights[tokenHash] = flight
	return flight, true
}

// finishRefreshFlight 发布刷新结果并唤醒等待者。
func (s *TokenService) finishRefreshFlight(tokenHash string, pair *TokenPair, err error) {
	s.rotateMu.Lock()
	flight, ok := s.refreshFlights[tokenHash]
	if ok {
		delete(s.refreshFlights, tokenHash)
		flight.pair = pair
		flight.err = err
	}
	s.rotateMu.Unlock()
	if ok {
		// 关闭前已写入结果，等待方读取到的是完整的 happens-before 结果。
		close(flight.done)
	}
}

// waitForRefreshFlight 等待并发刷新完成并返回它的结果。
func (s *TokenService) waitForRefreshFlight(flight *refreshFlight) (*TokenPair, error) {
	if flight == nil {
		return nil, ErrInvalidRefreshToken
	}
	<-flight.done
	if flight.err != nil {
		return nil, flight.err
	}
	if flight.pair == nil {
		return nil, ErrInvalidRefreshToken
	}
	return flight.pair, nil
}

// rememberRotation 记录一次轮换的结果，并回收已过期的条目。
func (s *TokenService) rememberRotation(tokenHash, userID string, pair *TokenPair) {
	if s == nil || pair == nil || strings.TrimSpace(tokenHash) == "" {
		return
	}
	now := s.currentTime()
	s.rotateMu.Lock()
	defer s.rotateMu.Unlock()
	if s.rotations == nil {
		s.rotations = make(map[string]rotatedRefreshToken)
	}
	for hash, entry := range s.rotations {
		if !now.Before(entry.expiresAt) {
			delete(s.rotations, hash)
		}
	}
	s.rotations[tokenHash] = rotatedRefreshToken{
		userID:    userID,
		pair:      pair,
		expiresAt: now.Add(refreshTokenReuseGrace),
	}
}

// reusedRotation 在宽限期内返回同一次轮换的令牌对。
func (s *TokenService) reusedRotation(tokenHash, userID string) (*TokenPair, bool) {
	if s == nil {
		return nil, false
	}
	now := s.currentTime()
	s.rotateMu.Lock()
	defer s.rotateMu.Unlock()
	entry, ok := s.rotations[tokenHash]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		delete(s.rotations, tokenHash)
		return nil, false
	}
	if userID != "" && entry.userID != "" && entry.userID != userID {
		return nil, false
	}
	return entry.pair, true
}

// forgetRotationsForUser 丢弃某个用户的复用条目。
// 显式登出/被踢下线必须立即生效，不能靠宽限期内的旧令牌继续换新。
func (s *TokenService) forgetRotationsForUser(userID string) {
	if s == nil || strings.TrimSpace(userID) == "" {
		return
	}
	s.rotateMu.Lock()
	defer s.rotateMu.Unlock()
	for hash, entry := range s.rotations {
		if entry.userID == userID {
			delete(s.rotations, hash)
		}
	}
}
