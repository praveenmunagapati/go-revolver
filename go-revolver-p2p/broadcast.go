/**
 * File        : broadcast.go
 * Description : Artifact broadcasting module.
 * Copyright   : Copyright (c) 2017 DFINITY Stiftung. All rights reserved.
 * Maintainer  : Enzo Haussecker <enzo@string.technology>
 * Stability   : Experimental
 */

package p2p

import (
	"io"
	"sort"

	"gx/ipfs/QmUEoLmhwH2CkiwHkfHVNeHm9WtMAxTh7jjUQAMRs1rNDe/go-revolver-util"
	"gx/ipfs/QmXYjuNuxVzXKJCfWasQk1RqkhVLDM9jtUKhqc2WPQmFSB/go-libp2p-peer"
	"gx/ipfs/QmZMeWroC7S4X8183A3TyGrzgsGjMWaBMedXyox1GUAvej/go-revolver-artifact"
)

// Activate the artifact broadcast.
func (client *client) activateBroadcast() func() {

	// Create a shutdown function.
	notify := make(chan struct{})
	shutdown := func() {
		close(notify)
	}

	// Broadcast artifacts from the send queue.
	go func() {
		for {
			select {
			case <-notify:
				return
			case data := <-client.send:
				object := artifact.FromBytes(data)
				client.artifacts.Add(object.Checksum(), object.Size())
				client.broadcast(object)
			}
		}
	}()

	// Return the shutdown function.
	return shutdown

}

// Broadcast an artifact.
func (client *client) broadcast(artifact artifact.Artifact) {

	// Get the artifact metadata.
	var metadata [44]byte

	checksum := artifact.Checksum()
	size := util.EncodeBigEndianUInt32(artifact.Size())
	timestamp := util.EncodeBigEndianInt64(artifact.Timestamp().UTC().UnixNano())

	copy(metadata[00:], checksum[:])
	copy(metadata[32:], size[:])
	copy(metadata[36:], timestamp[:])

	// Calculate the number of chunks to transfer.
	chunks := int((artifact.Size()+client.config.ArtifactChunkSize-1)/
		client.config.ArtifactChunkSize + 1)

	// Create a sorted exclude list from the witness cache.
	var exclude peer.IDSlice
	witnesses, exists := client.witnesses.Get(checksum)
	if exists {
		for _, id := range witnesses.([]peer.ID) {
			exclude = append(exclude, id)
		}
	}
	sort.Sort(exclude)

	// Send the artifact metadata to those who have not seen it.
	errors := make([]map[peer.ID]chan error, chunks)
	errors[0] = client.streamstore.Apply(
		func(peerId peer.ID, writer io.Writer) error {
			return util.WriteWithTimeout(
				writer,
				metadata[:],
				client.config.Timeout,
			)
		},
		exclude,
	)

	// Send the artifact in chunks.
	leftover := artifact.Size()
	for i := 1; i < chunks; i++ {

		// Create a chunk.
		var data []byte
		if leftover < client.config.ArtifactChunkSize {
			data = make([]byte, leftover)
			leftover = 0
		} else {
			data = make([]byte, client.config.ArtifactChunkSize)
			leftover -= client.config.ArtifactChunkSize
		}
		_, err := io.ReadFull(artifact, data)
		if err != nil {
			client.logger.Warning("Cannot read artifact")
			artifact.Disconnect()
			return
		}

		// Send the chunk to those who received the previous chunk.
		previous := errors[i-1]
		errors[i] = client.streamstore.Apply(
			func(peerId peer.ID, writer io.Writer) error {
				result, exists := previous[peerId]
				if exists {
					err := <-result
					if err != nil {
						return err
					}
					return util.WriteWithTimeout(
						writer,
						data,
						client.config.Timeout,
					)
				}
				return nil
			},
			exclude,
		)

	}

	// Remove anyone who failed to receive the artifact.
	for peerId, result := range errors[chunks-1] {
		go func(peerId peer.ID, result chan error) {
			pid := peerId
			err := <-result
			if err != nil {
				client.logger.Debug(pid, "failed to receive the artifact", err)
				client.streamstore.Remove(pid)
			}
		}(peerId, result)
	}

	// Close the artifact.
	artifact.Close()

}
