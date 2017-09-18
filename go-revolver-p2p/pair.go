/**
 * File        : pair.go
 * Description : Service for pairing artifact streams.
 * Copyright   : Copyright (c) 2017 DFINITY Stiftung. All rights reserved.
 * Maintainer  : Enzo Haussecker <enzo@string.technology>
 * Stability   : Experimental
 */

package p2p

import (
	"fmt"

	"gx/ipfs/QmUEoLmhwH2CkiwHkfHVNeHm9WtMAxTh7jjUQAMRs1rNDe/go-revolver-util"
	"gx/ipfs/QmVU26BGUSt3LkbWmoH7dP16mNz5VVRg4hDmWZBHAkq97w/go-libp2p-kbucket"
	"gx/ipfs/QmXYjuNuxVzXKJCfWasQk1RqkhVLDM9jtUKhqc2WPQmFSB/go-libp2p-peer"
	"gx/ipfs/QmahYsGWry85Y7WUe2SX5G4JkH2zifEQAUtJVLZ24aC9DF/go-libp2p-net"
)

const (
	ack = 0x06
	nak = 0x15
)

// Request to exchange artifacts with a peer.
func (client *client) pair(peerId peer.ID) (bool, error) {

	// Log this action.
	pid := peerId
	client.logger.Debug("Requesting to pair with", pid)

	// Connect to the target peer.
	stream, err := client.host.NewStream(
		client.context,
		pid,
		client.protocol+"/pair",
	)
	if err != nil {
		addrs := client.peerstore.PeerInfo(pid).Addrs
		client.logger.Debug("Cannot connect to", pid, "at", addrs, err)
		client.peerstore.SetAddrs(pid, addrs, 0)
		client.table.Remove(pid)
		return false, err
	}

	// Receive data from the target peer.
	data, err := util.ReadWithTimeout(
		stream,
		1,
		client.config.Timeout,
	)
	if err != nil {
		client.logger.Warning("Cannot receive data from", pid, err)
		stream.Close()
		return false, err
	}

	// Add the stream to the stream store.
	var success bool
	if data[0] == ack && client.streamstore.Add(pid, stream) {

		// Ready to exchange artifacts.
		client.logger.Debug("Ready to exchange artifacts with", pid)
		go client.process(stream)
		success = true

	} else {

		// Cannot pair with the target peer.
		client.logger.Debug("Cannot pair with", pid)
		stream.Close()
		success = false

	}

	// Return without error.
	return success, nil

}

// Handle incomming pairing requests.
func (client *client) pairHandler(stream net.Stream) {

	// Log this action.
	pid := stream.Conn().RemotePeer()
	client.logger.Debug("Receiving request to pair with", pid)

	// Prepare to reject the request.
	reject := func(reason ...interface{}) {
		client.logger.Debug("Cannot pair with", pid, "because", fmt.Sprint(reason...))
		err := util.WriteWithTimeout(
			stream,
			[]byte{nak},
			client.config.Timeout,
		)
		if err != nil {
			client.logger.Warning("Cannot send data to", pid, err)
		}
		stream.Close()
	}

	// Check if the peer is closer than others in its bucket.
	buckets := client.table.Buckets
	targets := deal(client.streamstore.Capacity(), len(buckets))
	for i := range buckets {

		if buckets[i].Has(pid) {

			// Select peers from this bucket.
			peers := client.streamstore.Peers()
			for j := 0; j < len(peers); j++ {
				if !buckets[i].Has(peers[j]) {
					copy(peers[j:], peers[j+1:])
					peers = peers[:len(peers)-1]
					j--
				}
			}

			if len(peers)+1 > targets[i] {

				// Sort the peers and select the overflow.
				overflow := kbucket.SortClosestPeers(
					append(peers, pid),
					kbucket.ConvertPeerID(client.id),
				)[targets[i]:]

				// Check if the peer exists in the overflow.
				for k := range overflow {
					if overflow[k] == pid {
						reject("closer peers exist in its bucket")
						return
					}
				}

				// Create space for the stream.
				client.streamstore.Remove(overflow[len(overflow)-1])

			}

			// Add the stream to the stream store.
			if !client.streamstore.Add(pid, stream) {
				reject(pid, " cannot be added to the stream store")
				return
			}

			// Send an acknowledgement.
			err := util.WriteWithTimeout(
				stream,
				[]byte{ack},
				client.config.Timeout,
			)
			if err != nil {
				client.logger.Warning("Cannot send data to", pid, err)
				client.streamstore.Remove(pid)
				return
			}

			// Ready to exchange artifacts.
			client.logger.Debug("Ready to exchange artifacts with", pid)
			go client.process(stream)
			return

		}

	}

	reject(pid, " does not exist in any bucket")

}

// Register the pairing handler.
func (client *client) registerPairService() {
	uri := client.protocol + "/pair"
	client.host.SetStreamHandler(uri, client.pairHandler)
}
