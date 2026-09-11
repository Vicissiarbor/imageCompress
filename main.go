package main

import (
	"bytes"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	xdraw "golang.org/x/image/draw"
)

func main(){
	rounter := gin.Default()
	rounter.MaxMultipartMemory = 16 << 20
	
	rounter.POST("/compress", compressHardler)

	rounter.Run(":46939")
}

func compressHardler(c *gin.Context){
	file, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error":err.Error()})
		return
	}
	
	log.Println(file.Filename)

	f, err := file.Open()
	if err != nil{
		c.JSON(http.StatusBadRequest, gin.H{"error":err.Error()})
		return 
	}
	defer f.Close()

	src, err := io.ReadAll(io.LimitReader(f, 8<< 20))
	if err != nil{
		c.JSON(http.StatusBadRequest, gin.H{"error":err.Error()})
		return
	}
	scale := 1.0	
	scaleStr := c.Query("scale")
	if scaleStr != ""{
		scale, err = strconv.ParseFloat(scaleStr, 64)
		if scale <= 0 || scale > 1.0 {
			err = errors.New("scale out size.")
		}
		if err != nil{
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	
	img, format, err := image.Decode(bytes.NewReader(src))
	
	if scale != 1.0{
		b := img.Bounds()
		w := int(float64(b.Dx())*scale)
		h := int(float64(b.Dy())*scale)

		w = max(w, 1)
		h = max(h, 1)

		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
		img = dst
	}	

	var buf bytes.Buffer
	var mime string
	switch format {
	case "png":
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		err = enc.Encode(&buf, img)
		mime = "image/png"
	case "gif":
		err = gif.Encode(&buf, img, nil)
		mime = "image/gif"
	default:
		qualityStr := c.Query("scale")
		quality, err := strconv.Atoi(qualityStr)
		if quality < 1 || quality > 100 {
			err = errors.New("quality out size.")
		}
		if err != nil{
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
		mime = "image/jpeg"
	}
	if err != nil{
		c.JSON(http.StatusBadRequest, gin.H{"error":err.Error()})
		return
	}

	c.Data(http.StatusOK, mime, buf.Bytes())
}
