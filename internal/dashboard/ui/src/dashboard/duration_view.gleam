import dashboard/format as f
import lustre/attribute as a
import lustre/element.{type Element, text}
import lustre/element/html as h

pub fn label(class: String, value: String) -> Element(msg) {
  h.span([a.class(class)], [text(f.duration_label(value))])
}
