package common

import (
	"github.com/bogem/id3v2"
	"github.com/winterssy/music-get/config"
	"github.com/winterssy/music-get/utils"
	"github.com/winterssy/music-get/utils/logger"
	"gopkg.in/cheggaaa/pb.v1"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	NeteaseMusic = 1000 + iota
	TencentMusic
)

const (
	DownloadSuccess = 2000 + iota
	DownloadAlready
	DownloadNoCopyrightError
	DownloadBuildPathError
	DownloadHTTPRequestError
	DownloadBuildFileError
	DownloadFileTransferError
)

type Tag struct {
	Title      string
	Artist     string
	Album      string
	Year       string
	Track      string
	CoverImage string
}

type MP3 struct {
	FileName    string
	SavePath    string
	Playable    bool
	DownloadURL string
	Tag         Tag
	Origin      int
}

type DownloadTask struct {
	MP3    *MP3
	Status int
}

func writeCoverImage(tag *id3v2.Tag, coverImage string, origin int) error {
	resp, err := Request("GET", coverImage, nil, nil, origin)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	pic := id3v2.PictureFrame{
		Encoding:    id3v2.EncodingUTF8,
		MimeType:    "image/jpg",
		PictureType: id3v2.PTOther,
		Picture:     data,
	}
	tag.AddAttachedPicture(pic)
	return nil
}

func (m *MP3) UpdateTag(wg *sync.WaitGroup) {
	var err error
	defer func() {
		if err != nil {
			logger.Error.Printf("Update music tag error: %s: %s", m.FileName, err.Error())
		}
		wg.Done()
	}()

	file := filepath.Join(m.SavePath, m.FileName)
	tag, err := id3v2.Open(file, id3v2.Options{Parse: true})
	if err != nil {
		return
	}
	defer tag.Close()

	tag.SetDefaultEncoding(id3v2.EncodingUTF8)
	tag.SetTitle(m.Tag.Title)
	tag.SetArtist(m.Tag.Artist)
	tag.SetAlbum(m.Tag.Album)
	tag.SetYear(m.Tag.Year)
	textFrame := id3v2.TextFrame{
		Encoding: id3v2.EncodingUTF8,
		Text:     m.Tag.Track,
	}
	tag.AddFrame(tag.CommonID("Track number/Position in set"), textFrame)

	if picURL, _ := url.Parse(m.Tag.CoverImage); picURL != nil {
		if err = writeCoverImage(tag, m.Tag.CoverImage, m.Origin); err != nil {
			logger.Warning.Printf("Update music cover image error: %s: %s", m.FileName, err.Error())
		}
	}

	if err = tag.Save(); err == nil {
		logger.Info.Printf("Music tag updated: %s", m.FileName)
	}
	return
}

func (m *MP3) SingleDownload() (status int) {
	defer func() {
		switch status {
		case DownloadSuccess:
			logger.Info.Print("Download complete")
		case DownloadNoCopyrightError:
			logger.Info.Printf("Ignore no coypright music: %s", m.Tag.Title)
		case DownloadAlready:
			logger.Info.Printf("Ignore already downloaded music: %s", m.Tag.Title)
		default:
			logger.Error.Printf("Download error: %d", status)
		}
	}()

	if !m.Playable {
		return DownloadNoCopyrightError
	}

	m.SavePath = filepath.Join(config.MP3DownloadDir, m.SavePath)
	if err := utils.BuildPathIfNotExist(m.SavePath); err != nil {
		return DownloadBuildPathError
	}

	fPath := filepath.Join(m.SavePath, m.FileName)
	if !config.DownloadOverwrite {
		if downloaded, _ := utils.ExistsPath(fPath); downloaded {
			return DownloadAlready
		}
	}

	logger.Info.Printf("Downloading: %s", m.FileName)
	resp, err := Request("GET", m.DownloadURL, nil, nil, m.Origin)
	if err != nil {
		return DownloadHTTPRequestError
	}
	defer resp.Body.Close()

	f, err := os.Create(fPath)
	if err != nil {
		return DownloadBuildFileError
	}
	defer f.Close()

	bar := pb.New(int(resp.ContentLength)).SetUnits(pb.U_BYTES).SetRefreshRate(100 * time.Millisecond)
	bar.ShowSpeed = true
	bar.Start()
	reader := bar.NewProxyReader(resp.Body)
	n, err := io.Copy(f, reader)
	if err != nil || n != resp.ContentLength {
		return DownloadFileTransferError
	}

	bar.Finish()
	return DownloadSuccess
}

func (m *MP3) ConcurrentDownload(taskList chan DownloadTask, taskQueue chan struct{}, wg *sync.WaitGroup) {
	var err error
	task := DownloadTask{
		MP3: m,
	}

	defer func() {
		if err != nil {
			logger.Error.Printf("Download error: %s: %s", m.FileName, err.Error())
		}
		wg.Done()
		taskList <- task
		<-taskQueue
	}()

	if !m.Playable {
		logger.Info.Printf("Ignore no coypright music: %s", m.Tag.Title)
		task.Status = DownloadNoCopyrightError
		return
	}

	m.SavePath = filepath.Join(config.MP3DownloadDir, m.SavePath)
	if err = utils.BuildPathIfNotExist(m.SavePath); err != nil {
		task.Status = DownloadBuildPathError
		return
	}

	fPath := filepath.Join(m.SavePath, m.FileName)
	if !config.DownloadOverwrite {
		if downloaded, _ := utils.ExistsPath(fPath); downloaded {
			logger.Info.Printf("Ignore already downloaded music: %s", m.Tag.Title)
			task.Status = DownloadAlready
			return
		}
	}

	logger.Info.Printf("Downloading: %s", m.FileName)
	resp, err := Request("GET", m.DownloadURL, nil, nil, m.Origin)
	if err != nil {
		task.Status = DownloadHTTPRequestError
		return
	}
	defer resp.Body.Close()

	f, err := os.Create(fPath)
	if err != nil {
		task.Status = DownloadBuildFileError
		return
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		task.Status = DownloadFileTransferError
		return
	}
	if n != resp.ContentLength {
		task.Status = DownloadFileTransferError
		return
	}

	logger.Info.Printf("Download complete: %s", m.FileName)
	task.Status = DownloadSuccess
	return
}
