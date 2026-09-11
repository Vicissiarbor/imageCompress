package main

import (
	"bytes"
	"errors"
	"image"
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
	if format == "jpeg"{
		qualityStr := c.DefaultQuery("scale", "1.0")
		if qualityStr != ""{
			quality, err := strconv.ParseFloat(qualityStr, 64)
			if quality < 1 || quality > 100 {
				err = errors.New("quality out size.")
			}
			if err != nil{
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
	}
	
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
}
