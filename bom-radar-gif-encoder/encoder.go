package bom_radar_gif_encoder

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/jlaffaye/ftp"

	log "github.com/sirupsen/logrus"
)

type BomRadarGifEncoder struct {
	prodID1 string
	prodID2 string
	gifData []byte
	tempFilesDir string
	client *ftp.ServerConn
	beVerbose bool
	writeTempFiles bool
}

func NewBomRadarGifEncoder(prodID1, prodID2, tempFilesDir string) (*BomRadarGifEncoder, error) {
	encoder := new(BomRadarGifEncoder)

	encoder.prodID1 = prodID1
	encoder.prodID2 = prodID2
	encoder.gifData = make([]byte, 0)
	encoder.tempFilesDir = tempFilesDir

	client, err := ftp.Dial("ftp.bom.gov.au:21")
	if err != nil {
		return nil, errors.New(fmt.Sprintf("could not establish ftp connection: %v", err))
	}
	encoder.client = client

	encoder.beVerbose = false
	encoder.writeTempFiles = false

	return encoder, nil
}

func (enc *BomRadarGifEncoder) Close() {
	if enc.client != nil {
		enc.client.Quit()
	}
}

func (enc *BomRadarGifEncoder) ToggleVerbosity() {
	enc.beVerbose = !enc.beVerbose
}

func (enc *BomRadarGifEncoder) ToggleTempFiles() {
	enc.writeTempFiles = !enc.writeTempFiles
}

func (enc *BomRadarGifEncoder) ListCurrentDirectory() (string, error) {
	currFTPDir, err := enc.client.CurrentDir()
	if err != nil {
		return "", errors.New(fmt.Sprintf("could not get the current dir: %v", err))
	}
	log.Infof("currently in this dir: %s", currFTPDir)

	if enc.beVerbose {
		names, err := enc.client.NameList("")
		if err != nil {
			return currFTPDir, errors.New(fmt.Sprintf("could not get file names in the current dir: %v", err))
		}
		log.Infof("listing file names in this dir: %s", names)
	}

	return currFTPDir, nil
}

func (enc *BomRadarGifEncoder) ChangeDirectory(path string) (string, error) {
	err := enc.client.ChangeDir(path)
	if err != nil {
		return "", errors.New(fmt.Sprintf("could not change to the new dir: %v", err))
	}

	newFTPDir, err := enc.ListCurrentDirectory()
	if err != nil {
		return "", err
	}

	return newFTPDir, nil
}


func (enc *BomRadarGifEncoder) MakeGif() ([]byte, error) {
	if enc.client == nil {
		return nil, errors.New(fmt.Sprintf("the FTP client was not initialized"))
	}

	// log on
	err := enc.client.Login("anonymous", "guest")
	if err != nil {
		return nil, errors.New(fmt.Sprintf("the FTP client could not log on: %v", err))
	}

	_, err = enc.ListCurrentDirectory()
	if err != nil {
		return nil, err
	}

	_, err = enc.ChangeDirectory("/")
	if err != nil {
		return nil, err
	}

	_, err = enc.ChangeDirectory("/anon/gen/radar_transparencies")
	if err != nil {
		return nil, err
	}

	// the following transparencies (in the form of png files) will be composited into a base
	// layer atop of which will sit the radar image
	transparencyLayerNames := [4]string{"background", "catchments", "waterways", "locations"}
	transparencyLayers := make([]image.Image, 0, 4)

	for _, layerName := range transparencyLayerNames {
		layerFileName := fmt.Sprintf("%s.%s.png", enc.prodID1, layerName)
		resp, err := enc.client.Retr(layerFileName)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("error retriving base layer file %s: %v", layerFileName, err))
		}

		var layerBytes bytes.Buffer
		layerByteWriter := bufio.NewWriter(&layerBytes)
		readSize, err := io.Copy(layerByteWriter, resp)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("error reading base layer file %s: %v", layerFileName, err))
		}
		fmt.Println(readSize)

		layerByteReader := bytes.NewReader(layerBytes.Bytes())
		img, _, err := image.Decode(layerByteReader)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("could not read layer file data into image %s: %v", layerFileName, err))
		}
		img.ColorModel().Convert(color.RGBA{})

		// close the response object to avoid getting into "extended passive mode"
		resp.Close()

		transparencyLayers = append(transparencyLayers, img)

		if enc.writeTempFiles {
			tempFile := fmt.Sprintf("%s%s_image.png", enc.tempFilesDir, layerName)
			out, err := os.Create(tempFile)
			if err != nil {
				log.Warnf("error creating temp file %s: %v", tempFile, err)
			}
			defer out.Close()

			err = png.Encode(out, img)
			log.Warnf("error writing temp file %s: %v", tempFile, err)
		}
	}

	backgroundLayer := transparencyLayers[0]
	combinedRGBA := image.NewRGBA(backgroundLayer.Bounds())
	combinedRGBA.ColorModel().Convert(color.RGBA{})
	backgroundLayerRect := image.Rectangle{
		Min: backgroundLayer.Bounds().Min,
		Max: backgroundLayer.Bounds().Max,
	}
	draw.Draw(combinedRGBA, backgroundLayerRect, backgroundLayer, backgroundLayer.Bounds().Min, draw.Src)

	for i := 1; i < len(transparencyLayers); i++ {
		newLayer := transparencyLayers[i]
		r := image.Rectangle{
			Min: newLayer.Bounds().Min,
			Max: newLayer.Bounds().Max,
		}
		draw.Draw(combinedRGBA, r, newLayer, newLayer.Bounds().Min, draw.Over)
	}

	// at this point we have a "combined RGBA image" which contains all the layers we wanted
	// the next thing to do is to download n most recent radar data layers and create n frames
	// each with the base layer (i.e. background plus other transparencies) plus the radar
	// data from 1 radar image

	_, err = enc.ChangeDirectory("/")
	if err != nil {
		return nil, err
	}

	_, err = enc.ChangeDirectory("/anon/gen/radar")
	if err != nil {
		return nil, err
	}

	names, err := enc.client.NameList("")
	if err != nil {
		return nil, errors.New(fmt.Sprintf("error getting the name list for dir /anon/gen/radar: %v", err))
	}

	relevantRadarFiles := make([]string, 0, 7)
	for _, fileName := range(names) {
		if strings.Contains(fileName, enc.prodID2) {
			relevantRadarFiles = append(relevantRadarFiles, fileName)
		}
	}

	if enc.beVerbose {
		log.Infof("relevant radar files: %v", relevantRadarFiles)
	}

	sort.SliceStable(relevantRadarFiles, func(i, j int) bool {
		iSplit := strings.Split(relevantRadarFiles[i], ".")
		iTs, _ := strconv.Atoi(iSplit[len(iSplit) - 2])

		jSplit := strings.Split(relevantRadarFiles[j], ".")
		jTs, _ := strconv.Atoi(jSplit[len(jSplit) - 2])

		return iTs < jTs
	})

	if enc.beVerbose {
		log.Infof("relevant radar files - sorted: %v", relevantRadarFiles)
	}

	radarLoopGif := gif.GIF{LoopCount: 7}
	for i := len(relevantRadarFiles) - 7; i < len(relevantRadarFiles); i++ {
		resp, err := enc.client.Retr(relevantRadarFiles[i])
		if err != nil {
			return nil, errors.New(fmt.Sprintf("error retrieving the radar data file %s: %v", relevantRadarFiles[i], err))
		}

		var radarBytes bytes.Buffer
		layerByteWriter := bufio.NewWriter(&radarBytes)
		readSize, err := io.Copy(layerByteWriter, resp)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("error reading the radar data %s: %v", relevantRadarFiles[i], err))
		}

		if enc.beVerbose {
			log.Infof("read the following number of bytes: %v", readSize)
		}

		layerByteReader := bytes.NewReader(radarBytes.Bytes())
		img, _, err := image.Decode(layerByteReader)
		if err != nil {
			return nil, errors.New(fmt.Sprintf("error reading the radar data into image %s: %v", relevantRadarFiles[i], err))
		}
		img.ColorModel().Convert(color.RGBA{})

		// close the response object to avoid getting into "extended passive mode"
		resp.Close()

		webSafePalette := palette.WebSafe
		webSafePalette = append(webSafePalette, image.Transparent)

		combinedTemp := image.NewPaletted(combinedRGBA.Bounds(), palette.WebSafe)

		draw.Draw(combinedTemp, backgroundLayerRect, combinedRGBA, combinedRGBA.Bounds().Min, draw.Src)
		draw.Draw(combinedTemp, backgroundLayerRect, img, combinedRGBA.Bounds().Min, draw.Over)

		// append the new "frame" to the gif
		radarLoopGif.Image = append(radarLoopGif.Image, combinedTemp)
		radarLoopGif.Delay = append(radarLoopGif.Delay, 50)
	}

	if enc.writeTempFiles {
		tempFile := fmt.Sprintf("%sbase_image.png", enc.tempFilesDir)
		out, err := os.Create(tempFile)
		if err != nil {
			log.Warnf("error creating temp file %s: %v", tempFile, err)
		}
		defer out.Close()

		err = png.Encode(out, combinedRGBA)
		log.Warnf("error writing temp file %s: %v", tempFile, err)

		tempGifFile := fmt.Sprintf("%sradar_loop.gif", enc.tempFilesDir)
		outGif, err := os.Create(tempGifFile)
		if err != nil {
			log.Warnf("error creating temp file %s: %v", tempGifFile, err)
		}
		defer outGif.Close()

		err = gif.EncodeAll(outGif, &radarLoopGif)
		if err != nil {
			log.Warnf("error writing temp gif file %s: %v", tempGifFile, err)
		}
	}

	// finally update the gif bytes in the encoder
	var gifBytes bytes.Buffer
	encGifWriter := bufio.NewWriter(&gifBytes)
	err = gif.EncodeAll(encGifWriter, &radarLoopGif)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("error updating encoder's radar gif bytes: %v", err))
	}
	enc.gifData = gifBytes.Bytes()

	return enc.gifData, nil
}
