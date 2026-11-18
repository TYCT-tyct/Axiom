import s from "./image-box.module.css";

// ImageBox shows an image the user supplied (a site logo, or one about to be uploaded) inside a fixed square, scaled
// to fit whole: an image of any shape keeps the box's size, so a tall one cannot push the form around it.
export function ImageBox({ src, size }: { src: string; size: 40 | 48 }) {
  return <img src={src} alt="" width={size} height={size} className={size === 40 ? s.s40 : s.s48} />;
}
