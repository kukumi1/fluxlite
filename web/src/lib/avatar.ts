export const AVATAR_SIZE = 128;

/**
 * 把用户选的图片变成一张 128px 的方形 PNG。
 *
 * 走一遍 canvas 有三个作用：把手机拍的几 MB 大图压到几 KB；剥掉 EXIF（里面
 * 可能带着拍摄地点）；以及重新编码 —— 原文件里夹带的任何东西都不会被传上去，
 * 上传的只是像素。服务端不会因此就信任结果，它会自己再解一遍。
 */
export async function toAvatarPNG(file: File): Promise<string> {
  const bitmap = await loadImage(file);
  try {
    const canvas = document.createElement("canvas");
    canvas.width = AVATAR_SIZE;
    canvas.height = AVATAR_SIZE;

    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("浏览器不支持 canvas，无法处理图片");

    // 居中裁成正方形，而不是把图压扁。
    const side = Math.min(bitmap.width, bitmap.height);
    const sx = (bitmap.width - side) / 2;
    const sy = (bitmap.height - side) / 2;
    ctx.imageSmoothingQuality = "high";
    ctx.drawImage(bitmap, sx, sy, side, side, 0, 0, AVATAR_SIZE, AVATAR_SIZE);

    return canvas.toDataURL("image/png");
  } finally {
    if ("close" in bitmap) bitmap.close();
  }
}

function loadImage(file: File): Promise<ImageBitmap | HTMLImageElement> {
  if ("createImageBitmap" in window) {
    return createImageBitmap(file);
  }
  // 老浏览器没有 createImageBitmap，退回到 <img> + object URL。
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      resolve(img);
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error("这个文件不是浏览器能识别的图片"));
    };
    img.src = url;
  });
}
