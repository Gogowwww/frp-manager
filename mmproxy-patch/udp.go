// Copyright 2019 Path Network, Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// PATCH (frp-manager) — gestion UDP « par session ».
//
// frp >= v0.67 n'insère l'en-tête PROXY protocol que sur le PREMIER datagramme
// de chaque session (côté frpc, un socket local distinct est ouvert par client,
// cf. udpConnMap dans pkg/proto/udp/udp.go). Le go-mmproxy amont exige l'en-tête
// sur CHAQUE datagramme et jette silencieusement les suivants.
//
// On mémorise donc l'adresse usurpée (saddr, issue de l'en-tête) par adresse
// source réelle du paquet (remoteAddr = le socket frpc de la session) : le 1er
// datagramme porte l'en-tête et fixe le mapping, les datagrammes suivants (sans
// en-tête) réutilisent l'adresse mémorisée. Le mapping est rafraîchi à chaque
// en-tête reçu (auto-réparation si frpc recycle un port source).
//
// Ce fichier corrige aussi un BUG upstream : udp.go amont sélectionne l'adresse
// cible via netip.MustParseAddr(downstreamAddr.String()) — or String() renvoie
// "IP:port" et MustParseAddr n'accepte pas de port → panic au 1er datagramme UDP
// (le service systemd redémarre en boucle, « l'UDP ne marche pas »). tcp.go, lui,
// utilise correctement MustParseAddrPort. On teste ici la famille via l'IP.
//
// Fichier remplacé à la compilation par mmproxy-patch/build.sh — le reste du
// paquet (Opts, PROXYReadRemoteAddr, GetBuffer, DialUpstreamControl, UDP,
// CheckOriginAllowed) est inchangé.

package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync/atomic"
	"syscall"
	"time"
)

type udpConnection struct {
	lastActivity   *int64
	clientAddr     *net.UDPAddr
	downstreamAddr *net.UDPAddr
	upstream       *net.UDPConn
	logger         *slog.Logger
	mapKey         string
}

func udpCloseAfterInactivity(conn *udpConnection, socketClosures chan<- string) {
	for {
		lastActivity := atomic.LoadInt64(conn.lastActivity)
		<-time.After(Opts.UDPCloseAfter)
		if atomic.LoadInt64(conn.lastActivity) == lastActivity {
			break
		}
	}
	conn.upstream.Close()
	socketClosures <- conn.mapKey
}

func udpCopyFromUpstream(downstream net.PacketConn, conn *udpConnection) {
	rawConn, err := conn.upstream.SyscallConn()
	if err != nil {
		conn.logger.Error("failed to retrieve raw connection from upstream socket", "error", err)
		return
	}

	var syscallErr error

	err = rawConn.Read(func(fd uintptr) bool {
		buf := GetBuffer()
		defer PutBuffer(buf)

		for {
			n, _, serr := syscall.Recvfrom(int(fd), buf, syscall.MSG_DONTWAIT)
			if errors.Is(serr, syscall.EWOULDBLOCK) {
				return false
			}
			if serr != nil {
				syscallErr = serr
				return true
			}
			if n == 0 {
				return true
			}

			atomic.AddInt64(conn.lastActivity, 1)

			if _, serr := downstream.WriteTo(buf[:n], conn.downstreamAddr); serr != nil {
				syscallErr = serr
				return true
			}
		}
	})

	if err == nil {
		err = syscallErr
	}
	if err != nil {
		conn.logger.Debug("failed to read from upstream", "error", err)
	}
}

// udpGetSocketFromMap indexe les sessions par mapKey (adresse source réelle du
// paquet), tandis que saddr (adresse usurpée) reste utilisée pour l'usurpation
// à la connexion upstream.
func udpGetSocketFromMap(downstream net.PacketConn, downstreamAddr, saddr net.Addr, mapKey string, logger *slog.Logger,
	connMap map[string]*udpConnection, socketClosures chan<- string) (*udpConnection, error) {
	if conn := connMap[mapKey]; conn != nil {
		atomic.AddInt64(conn.lastActivity, 1)
		return conn, nil
	}

	// CORRECTIF (frp-manager) : upstream utilise netip.MustParseAddr sur
	// downstreamAddr.String() (= "IP:port"), ce qui PANIQUE au 1er datagramme UDP
	// (MustParseAddr n'accepte pas de port ; cf. tcp.go qui utilise, lui,
	// MustParseAddrPort). On teste la famille via l'IP directement.
	targetAddr := Opts.TargetAddr6
	if downstreamAddr.(*net.UDPAddr).IP.To4() != nil {
		targetAddr = Opts.TargetAddr4
	}

	logger = logger.With(slog.String("downstreamAddr", downstreamAddr.String()), slog.String("targetAddr", targetAddr.String()))
	dialer := net.Dialer{LocalAddr: saddr}
	if saddr != nil {
		logger = logger.With(slog.String("clientAddr", saddr.String()))
		dialer.Control = DialUpstreamControl(saddr.(*net.UDPAddr).Port)
	}

	if Opts.Verbose > 1 {
		logger.Debug("new connection")
	}

	conn, err := dialer.Dial("udp", targetAddr.String())
	if err != nil {
		logger.Debug("failed to connect to upstream", "error", err)
		return nil, err
	}

	udpConn := &udpConnection{upstream: conn.(*net.UDPConn),
		logger:         logger,
		lastActivity:   new(int64),
		downstreamAddr: downstreamAddr.(*net.UDPAddr),
		mapKey:         mapKey}
	if saddr != nil {
		udpConn.clientAddr = saddr.(*net.UDPAddr)
	}

	go udpCopyFromUpstream(downstream, udpConn)
	go udpCloseAfterInactivity(udpConn, socketClosures)

	connMap[mapKey] = udpConn
	return udpConn, nil
}

func UDPListen(listenConfig *net.ListenConfig, logger *slog.Logger, errorsCh chan<- error) {
	ctx := context.Background()
	ln, err := listenConfig.ListenPacket(ctx, "udp", Opts.ListenAddr.String())
	if err != nil {
		logger.Error("failed to bind listener", "error", err)
		errorsCh <- err
		return
	}

	logger.Info("listening")

	socketClosures := make(chan string, 1024)
	connectionMap := make(map[string]*udpConnection)
	// Mapping adresse source réelle -> adresse usurpée, alimenté par l'en-tête
	// PROXY du 1er datagramme et réutilisé pour les datagrammes suivants.
	remoteToSaddr := make(map[string]*net.UDPAddr)

	buffer := GetBuffer()
	defer PutBuffer(buffer)

	for {
		n, remoteAddr, err := ln.ReadFrom(buffer)
		if err != nil {
			logger.Error("failed to read from socket", "error", err)
			continue
		}

		if !CheckOriginAllowed(remoteAddr.(*net.UDPAddr).IP) {
			logger.Debug("packet origin not in allowed subnets", slog.String("remoteAddr", remoteAddr.String()))
			continue
		}

		mapKey := remoteAddr.String()

		var saddr net.Addr
		var payload []byte
		hdrSaddr, _, restBytes, perr := PROXYReadRemoteAddr(buffer[:n], UDP)
		if perr == nil {
			// Datagramme avec en-tête PROXY (1er paquet d'une session, ou frp < 0.67).
			saddr = hdrSaddr
			payload = restBytes
			if ua, ok := hdrSaddr.(*net.UDPAddr); ok && ua != nil {
				remoteToSaddr[mapKey] = ua
			}
		} else if ua := remoteToSaddr[mapKey]; ua != nil {
			// Datagramme sans en-tête : suite d'une session connue (frp >= 0.67).
			saddr = ua
			payload = buffer[:n]
		} else {
			logger.Debug("no PROXY header and no known session for source",
				slog.String("remoteAddr", remoteAddr.String()), "error", perr)
			continue
		}

		for {
			doneClosing := false
			select {
			case mk := <-socketClosures:
				delete(connectionMap, mk)
				delete(remoteToSaddr, mk)
			default:
				doneClosing = true
			}
			if doneClosing {
				break
			}
		}

		conn, err := udpGetSocketFromMap(ln, remoteAddr, saddr, mapKey, logger, connectionMap, socketClosures)
		if err != nil {
			continue
		}

		if _, err = conn.upstream.Write(payload); err != nil {
			conn.logger.Error("failed to write to upstream socket", "error", err)
		}
	}
}
