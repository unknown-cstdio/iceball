package main

import (
	"bytes"
	"container/heap"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"

	"gitlab.torproject.org/tpo/anti-censorship/pluggable-transports/snowflake/v2/common/bridgefingerprint"

	"github.com/prometheus/client_golang/prometheus"
	"gitlab.torproject.org/tpo/anti-censorship/pluggable-transports/snowflake/v2/common/messages"
)

const (
	ClientTimeout = 10
	ProxyTimeout  = 10

	NATUnknown      = "unknown"
	NATRestricted   = "restricted"
	NATUnrestricted = "unrestricted"
)

type IPC struct {
	ctx *BrokerContext
}

func (i *IPC) Debug(_ interface{}, response *string) error {
	var unknowns int
	var natRestricted, natUnrestricted, natUnknown int
	proxyTypes := make(map[string]int)

	i.ctx.snowflakeLock.Lock()
	s := fmt.Sprintf("current snowflakes available: %d\n", len(i.ctx.idToSnowflake))
	for _, snowflake := range i.ctx.idToSnowflake {
		if messages.KnownProxyTypes[snowflake.proxyType] {
			proxyTypes[snowflake.proxyType]++
		} else {
			unknowns++
		}

		switch snowflake.natType {
		case NATRestricted:
			natRestricted++
		case NATUnrestricted:
			natUnrestricted++
		default:
			natUnknown++
		}

	}
	i.ctx.snowflakeLock.Unlock()

	for pType, num := range proxyTypes {
		s += fmt.Sprintf("\t%s proxies: %d\n", pType, num)
	}
	s += fmt.Sprintf("\tunknown proxies: %d", unknowns)

	s += fmt.Sprintf("\nNAT Types available:")
	s += fmt.Sprintf("\n\trestricted: %d", natRestricted)
	s += fmt.Sprintf("\n\tunrestricted: %d", natUnrestricted)
	s += fmt.Sprintf("\n\tunknown: %d", natUnknown)

	*response = s
	return nil
}

func (i *IPC) ProxyPolls(arg messages.Arg, response *[]byte) error {
	sid, proxyType, natType, clients, _, relayPatternSupported, err := messages.DecodeProxyPollRequestWithRelayPrefix(arg.Body)
	addr := arg.RemoteAddr
	log.Printf("proxy poll request from %s", addr)
	if err != nil {
		return messages.ErrBadRequest
	}

	if !relayPatternSupported {
		i.ctx.metrics.lock.Lock()
		i.ctx.metrics.proxyPollWithoutRelayURLExtension++
		i.ctx.metrics.promMetrics.ProxyPollWithoutRelayURLExtensionTotal.With(prometheus.Labels{"nat": natType, "type": proxyType}).Inc()
		i.ctx.metrics.lock.Unlock()
	} else {
		i.ctx.metrics.lock.Lock()
		i.ctx.metrics.proxyPollWithRelayURLExtension++
		i.ctx.metrics.promMetrics.ProxyPollWithRelayURLExtensionTotal.With(prometheus.Labels{"nat": natType, "type": proxyType}).Inc()
		i.ctx.metrics.lock.Unlock()
	}

	/*
		if !i.ctx.CheckProxyRelayPattern(relayPattern, !relayPatternSupported) {
			i.ctx.metrics.lock.Lock()
			i.ctx.metrics.proxyPollRejectedWithRelayURLExtension++
			i.ctx.metrics.promMetrics.ProxyPollRejectedForRelayURLExtensionTotal.With(prometheus.Labels{"nat": natType, "type": proxyType}).Inc()
			i.ctx.metrics.lock.Unlock()

			log.Printf("bad request: rejected relay pattern from proxy = %v", messages.ErrBadRequest)
			b, err := messages.EncodePollResponseWithRelayURL("", false, "", "", "incorrect relay pattern")
			*response = b
			if err != nil {
				return messages.ErrInternal
			}
			return nil
		}
	*/

	// Log geoip stats
	remoteIP, _, err := net.SplitHostPort(arg.RemoteAddr)
	if err != nil {
		log.Println("Warning: cannot process proxy IP: ", err.Error())
	} else {
		i.ctx.metrics.lock.Lock()
		i.ctx.metrics.UpdateCountryStats(remoteIP, proxyType, natType)
		i.ctx.metrics.RecordIPAddress(remoteIP)
		i.ctx.metrics.lock.Unlock()
	}

	i.ctx.snowflakeLock.Lock()
	Snowflake := &Snowflake{
		id:            sid,
		proxyType:     proxyType,
		ip:            addr,
		natType:       natType,
		offerChannel:  make(chan *ClientOffer),
		answerChannel: make(chan string),
		clients:       clients,
		index:         -1,
	}
	i.ctx.idToSnowflake[sid] = Snowflake
	if natType == NATUnrestricted {
		log.Printf("Proxy: Added unrestricted snowflake %s", sid)
		heap.Push(i.ctx.snowflakes, Snowflake)
	} else {
		log.Printf("Proxy: Added restricted snowflake %s", sid)
		heap.Push(i.ctx.restrictedSnowflakes, Snowflake)
	}
	i.ctx.snowflakeLock.Unlock()

	return nil

	/*
		var b []byte

		// Wait for a client to avail an offer to the snowflake, or timeout if nil.

		offer := i.ctx.RequestOffer(sid, proxyType, natType, clients)

		if offer == nil {
			i.ctx.metrics.lock.Lock()
			i.ctx.metrics.proxyIdleCount++
			i.ctx.metrics.promMetrics.ProxyPollTotal.With(prometheus.Labels{"nat": natType, "status": "idle"}).Inc()
			i.ctx.metrics.lock.Unlock()

			b, err = messages.EncodePollResponse("", false, "")
			if err != nil {
				return messages.ErrInternal
			}

			*response = b
			return nil
		}

		i.ctx.metrics.promMetrics.ProxyPollTotal.With(prometheus.Labels{"nat": natType, "status": "matched"}).Inc()
		var relayURL string
		bridgeFingerprint, err := bridgefingerprint.FingerprintFromBytes(offer.fingerprint)
		if err != nil {
			return messages.ErrBadRequest
		}
		if info, err := i.ctx.bridgeList.GetBridgeInfo(bridgeFingerprint); err != nil {
			return err
		} else {
			relayURL = info.WebSocketAddress
		}
		b, err = messages.EncodePollResponseWithRelayURL(string(offer.sdp), true, offer.natType, relayURL, "")
		if err != nil {
			return messages.ErrInternal
		}
		*response = b

		return nil*/
}

func sendClientResponse(resp *messages.ClientPollResponse, response *[]byte) error {
	data, err := resp.EncodePollResponse()
	if err != nil {
		log.Printf("error encoding answer")
		return messages.ErrInternal
	} else {
		*response = []byte(data)
		return nil
	}
}

func (i *IPC) ClientOffers(arg messages.Arg, response *[]byte) error {
	//startTime := time.Now()

	req, err := messages.DecodeClientPollRequest(arg.Body)
	if err != nil {
		return sendClientResponse(&messages.ClientPollResponse{Error: err.Error()}, response)
	}

	offer := &ClientOffer{
		NatType: req.NAT,
		Sdp:     []byte(req.Offer),
	}

	fingerprint, err := hex.DecodeString(req.Fingerprint)
	if err != nil {
		return sendClientResponse(&messages.ClientPollResponse{Error: err.Error()}, response)
	}

	BridgeFingerprint, err := bridgefingerprint.FingerprintFromBytes(fingerprint)
	if err != nil {
		return sendClientResponse(&messages.ClientPollResponse{Error: err.Error()}, response)
	}

	if _, err := i.ctx.GetBridgeInfo(BridgeFingerprint); err != nil {
		return err
	}

	offer.Fingerprint = BridgeFingerprint.ToBytes()

	snowflake := i.matchSnowflake(offer.NatType)
	if snowflake != nil {
		ip := snowflake.ip
		log.Printf("Client: Matched with %s", ip)
		offerJSON, err := json.Marshal(offer)
		log.Print(string(offerJSON))
		if err != nil {
			return sendClientResponse(&messages.ClientPollResponse{Error: err.Error()}, response)
		}
		resp, err := http.Post(ip, "/add", bytes.NewBuffer(offerJSON))
		if err != nil {
			return sendClientResponse(&messages.ClientPollResponse{Error: err.Error()}, response)
		}
		if resp.StatusCode != http.StatusOK {
			return sendClientResponse(&messages.ClientPollResponse{Error: messages.StrNoProxies}, response)
		}
		answer := messages.ClientPollResponse{}
		err = json.NewDecoder(resp.Body).Decode(&answer)
		log.Print(answer)
		if err != nil {
			return sendClientResponse(&messages.ClientPollResponse{Error: err.Error()}, response)
		}
		sendClientResponse(&answer, response)
		//snowflake.offerChannel <- offer
	} else {
		i.ctx.metrics.lock.Lock()
		i.ctx.metrics.clientDeniedCount++
		i.ctx.metrics.promMetrics.ClientPollTotal.With(prometheus.Labels{"nat": offer.NatType, "status": "denied"}).Inc()
		if offer.NatType == NATUnrestricted {
			i.ctx.metrics.clientUnrestrictedDeniedCount++
		} else {
			i.ctx.metrics.clientRestrictedDeniedCount++
		}
		i.ctx.metrics.lock.Unlock()
		resp := &messages.ClientPollResponse{Error: messages.StrNoProxies}
		return sendClientResponse(resp, response)
	}

	// Wait for the answer to be returned on the channel or timeout.
	/*
		select {
		case answer := <-snowflake.answerChannel:
			i.ctx.metrics.lock.Lock()
			i.ctx.metrics.clientProxyMatchCount++
			i.ctx.metrics.promMetrics.ClientPollTotal.With(prometheus.Labels{"nat": offer.natType, "status": "matched"}).Inc()
			i.ctx.metrics.lock.Unlock()
			resp := &messages.ClientPollResponse{Answer: answer}
			err = sendClientResponse(resp, response)
			// Initial tracking of elapsed time.
			i.ctx.metrics.clientRoundtripEstimate = time.Since(startTime) / time.Millisecond
		case <-time.After(time.Second * ClientTimeout):
			log.Println("Client: Timed out.")
			resp := &messages.ClientPollResponse{Error: messages.StrTimedOut}
			err = sendClientResponse(resp, response)
		}*/

	i.ctx.snowflakeLock.Lock()
	i.ctx.metrics.promMetrics.AvailableProxies.With(prometheus.Labels{"nat": snowflake.natType, "type": snowflake.proxyType}).Dec()
	delete(i.ctx.idToSnowflake, snowflake.id)
	i.ctx.snowflakeLock.Unlock()

	return err
}

func (i *IPC) matchSnowflake(natType string) *Snowflake {
	// Only hand out known restricted snowflakes to unrestricted clients
	var snowflakeHeap *SnowflakeHeap
	if natType == NATUnrestricted {
		snowflakeHeap = i.ctx.restrictedSnowflakes
	} else {
		snowflakeHeap = i.ctx.snowflakes
	}

	i.ctx.snowflakeLock.Lock()
	defer i.ctx.snowflakeLock.Unlock()

	if snowflakeHeap.Len() > 0 {
		return heap.Pop(snowflakeHeap).(*Snowflake)
	} else {
		return nil
	}
}

func (i *IPC) ProxyAnswers(arg messages.Arg, response *[]byte) error {
	answer, id, err := messages.DecodeAnswerRequest(arg.Body)
	if err != nil || answer == "" {
		return messages.ErrBadRequest
	}

	var success = true
	i.ctx.snowflakeLock.Lock()
	snowflake, ok := i.ctx.idToSnowflake[id]
	i.ctx.snowflakeLock.Unlock()
	if !ok || snowflake == nil {
		// The snowflake took too long to respond with an answer, so its client
		// disappeared / the snowflake is no longer recognized by the Broker.
		success = false
		log.Printf("Warning: matching with snowflake client failed")
	}

	b, err := messages.EncodeAnswerResponse(success)
	if err != nil {
		log.Printf("Error encoding answer: %s", err.Error())
		return messages.ErrInternal
	}
	*response = b

	if success {
		snowflake.answerChannel <- answer
	}

	return nil
}
