package reisen

// #cgo pkg-config: libavformat libavcodec libavutil libswscale
// #include <libavcodec/avcodec.h>
// #include <libavformat/avformat.h>
// #include <libavutil/avconfig.h>
// #include <libswscale/swscale.h>
// #include <libavcodec/bsf.h>
import "C"

import (
	"fmt"
	"time"
	"unsafe"
)

// Media is a media file containing
// audio, video and other types of streams.
type Media struct {
	ctx     *C.AVFormatContext
	packet  *C.AVPacket
	streams []Stream
}

// StreamCount returns the number of streams.
func (media *Media) StreamCount() int {
	return int(media.ctx.nb_streams)
}

// Streams returns a slice of all the available
// media data streams.
func (media *Media) Streams() []Stream {
	streams := make([]Stream, len(media.streams))
	copy(streams, media.streams)

	return streams
}

// VideoStreams returns all the
// video streams of the media file.
func (media *Media) VideoStreams() []*VideoStream {
	videoStreams := []*VideoStream{}

	for _, stream := range media.streams {
		if videoStream, ok := stream.(*VideoStream); ok {
			videoStreams = append(videoStreams, videoStream)
		}
	}

	return videoStreams
}

// AudioStreams returns all the
// audio streams of the media file.
func (media *Media) AudioStreams() []*AudioStream {
	audioStreams := []*AudioStream{}

	for _, stream := range media.streams {
		if audioStream, ok := stream.(*AudioStream); ok {
			audioStreams = append(audioStreams, audioStream)
		}
	}

	return audioStreams
}

// Duration returns the overall duration
// of the media file.
func (media *Media) Duration() (time.Duration, error) {
	dur := media.ctx.duration
	tm := float64(dur) / float64(TimeBase)

	return time.ParseDuration(fmt.Sprintf("%fs", tm))
}

// FormatName returns the name of the media format.
func (media *Media) FormatName() string {
	if media.ctx.iformat.name == nil {
		return ""
	}

	return C.GoString(media.ctx.iformat.name)
}

// FormatLongName returns the long name
// of the media container.
func (media *Media) FormatLongName() string {
	if media.ctx.iformat.long_name == nil {
		return ""
	}

	return C.GoString(media.ctx.iformat.long_name)
}

// FormatMIMEType returns the MIME type name
// of the media container.
func (media *Media) FormatMIMEType() string {
	if media.ctx.iformat.mime_type == nil {
		return ""
	}

	return C.GoString(media.ctx.iformat.mime_type)
}

// findStreams retrieves the stream information
// from the media container.
func (media *Media) findStreams() error {
	streams := []Stream{}
	status := C.avformat_find_stream_info(media.ctx, nil)

	if status < 0 {
		return fmt.Errorf(
			"couldn't find stream information")
	}

	// CRITICAL FIX: Use manual pointer arithmetic instead of unsafe.Slice.
	// unsafe.Slice can panic or return garbage if the C memory layout 
	// doesn't perfectly match what Go expects for a slice header.
	if media.ctx.nb_streams > 0 && media.ctx.streams != nil {
		// media.ctx.streams is a **AVStream (array of pointers)
		basePtr := uintptr(unsafe.Pointer(media.ctx.streams))
		ptrSize := unsafe.Sizeof(*media.ctx.streams)

		for i := 0; i < int(media.ctx.nb_streams); i++ {
			// *(basePtr + i * ptrSize)
			elementAddr := basePtr + uintptr(i)*ptrSize
			innerStream := *(**C.AVStream)(unsafe.Pointer(elementAddr))

			if innerStream == nil {
				continue
			}

			codecParams := innerStream.codecpar
			codec := C.avcodec_find_decoder(codecParams.codec_id)

			if codec == nil {
				unknownStream := new(UnknownStream)
				unknownStream.inner = innerStream
				unknownStream.codecParams = codecParams
				unknownStream.media = media

				streams = append(streams, unknownStream)
				continue
			}

			switch codecParams.codec_type {
			case C.AVMEDIA_TYPE_VIDEO:
				videoStream := new(VideoStream)
				videoStream.inner = innerStream
				videoStream.codecParams = codecParams
				videoStream.codec = codec
				videoStream.media = media
				streams = append(streams, videoStream)

			case C.AVMEDIA_TYPE_AUDIO:
				audioStream := new(AudioStream)
				audioStream.inner = innerStream
				audioStream.codecParams = codecParams
				audioStream.codec = codec
				audioStream.media = media
				streams = append(streams, audioStream)

			default:
				unknownStream := new(UnknownStream)
				unknownStream.inner = innerStream
				unknownStream.codecParams = codecParams
				unknownStream.codec = codec
				unknownStream.media = media
				streams = append(streams, unknownStream)
			}
		}
	}

	media.streams = streams

	return nil
}

// OpenDecode opens the media container for decoding.
func (media *Media) OpenDecode() error {
	media.packet = C.av_packet_alloc()

	if media.packet == nil {
		return fmt.Errorf(
			"couldn't allocate a new packet")
	}

	return nil
}

// ReadPacket reads the next packet from the media stream.
func (media *Media) ReadPacket() (*Packet, bool, error) {
	status := C.av_read_frame(media.ctx, media.packet)

	if status < 0 {
		if status == C.int(ErrorAgain) {
			return nil, true, nil
		}
		// No packets anymore.
		return nil, false, nil
	}

	// Safety check for stream index bounds
	streamIdx := int(media.packet.stream_index)
	if streamIdx < 0 || streamIdx >= len(media.streams) {
		// Packet from a stream we didn't index (or index mismatch)
		return nil, true, nil
	}

	// Filter the packet if needed.
	packetStream := media.streams[streamIdx]
	outPacket := media.packet

	if packetStream.filter() != nil {
		filter := packetStream.filter()
		packetIn := packetStream.filterIn()
		packetOut := packetStream.filterOut()

		status = C.av_packet_ref(packetIn, media.packet)

		if status < 0 {
			return nil, false,
				fmt.Errorf("%d: couldn't reference the packet",
					status)
		}

		status = C.av_bsf_send_packet(filter, packetIn)

		if status < 0 {
			return nil, false,
				fmt.Errorf("%d: couldn't send the packet to the filter",
					status)
		}

		status = C.av_bsf_receive_packet(filter, packetOut)

		if status < 0 {
			return nil, false,
				fmt.Errorf("%d: couldn't receive the packet from the filter",
					status)
		}

		outPacket = packetOut
	}

	return newPacket(media, outPacket), true, nil
}

// CloseDecode closes the media container for decoding.
func (media *Media) CloseDecode() error {
	if media.packet != nil {
		C.av_free(unsafe.Pointer(media.packet))
		media.packet = nil
	}
	return nil
}

// Close closes the media container.
func (media *Media) Close() {
	if media.ctx != nil {
		// Use close_input to properly free the context and IO
		C.avformat_close_input(&media.ctx)
		media.ctx = nil
	}
}

// NewMedia returns a new media container analyzer
// for the specified media file.
func NewMedia(filename string) (*Media, error) {
	// CRITICAL FIX: Do NOT use avformat_alloc_context() here.
	// Let avformat_open_input allocate it by passing nil.
	// If you alloc it manually and then open input, FFmpeg can 
	// get into a corrupted state if the alloc version differs slightly 
	// or if open_input overwrites flags incorrectly.
	media := &Media{
		ctx: nil,
	}

	fname := C.CString(filename)
	// We must free the C string after usage
	defer C.free(unsafe.Pointer(fname))

	status := C.avformat_open_input(&media.ctx, fname, nil, nil)

	if status < 0 {
		return nil, fmt.Errorf(
			"couldn't open file %s", filename)
	}

	err := media.findStreams()
	if err != nil {
		C.avformat_close_input(&media.ctx)
		return nil, err
	}

	return media, nil
}