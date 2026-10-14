import { Controller } from "@hotwired/stimulus"

// The public sign-up form. Everything here is a convenience: the server
// checks it all again, and the form works without this file.
//
//   - follow-up questions (data-signup-when="12 15": choice IDs) show only
//     while one of those choices is picked
//   - a choice that can't be picked with a picked one (data-excludes) is
//     greyed out with "Can't pick with Make chili"
//   - Add another person clones the last person block and renumbers its
//     fields (p2_name -> p3_name); it replaces the no-JS "Add more people"
//
// Full choices are disabled by the server (data-full) and stay that way.
export default class extends Controller {
  static targets = ["followup", "add", "more", "count", "people"]

  connect() {
    if (this.hasAddTarget) {
      this.addTarget.hidden = false
      if (this.hasMoreTarget) this.moreTarget.hidden = true
    }
    this.update()
  }

  persons() {
    return [...this.element.querySelectorAll("[data-signup-person]")]
  }

  // Choice inputs in a scope: a person block, or null for the rest.
  choices(scope) {
    return [...this.element.querySelectorAll("input[data-choice]")].filter(
      (i) => i.closest("[data-signup-person]") === scope,
    )
  }

  pickedIn(scope) {
    return new Set(this.choices(scope).filter((i) => i.checked).map((i) => i.dataset.choice))
  }

  update() {
    const general = this.pickedIn(null)
    const persons = this.persons()
    const perPerson = new Map(persons.map((p) => [p, this.pickedIn(p)]))

    this.followupTargets.forEach((el) => {
      const person = el.closest("[data-signup-person]")
      const picked = new Set(general)
      if (person) perPerson.get(person).forEach((id) => picked.add(id))
      else perPerson.forEach((set) => set.forEach((id) => picked.add(id)))
      el.hidden = !el.dataset.signupWhen.split(" ").some((id) => picked.has(id))
    })

    ;[null, ...persons].forEach((scope) => {
      const inputs = this.choices(scope)
      const picked = scope ? perPerson.get(scope) : general
      const labelOf = new Map(inputs.map((i) => [i.dataset.choice, i.closest("label").querySelector("span")?.textContent]))
      inputs.forEach((input) => {
        const note = input.closest("label").querySelector("[data-note]")
        if (input.hasAttribute("data-full")) return
        const blocker = (input.dataset.excludes || "").split(" ").find((id) => id && picked.has(id))
        const block = blocker !== undefined && !input.checked
        input.disabled = block
        if (note) note.textContent = block ? `Can't pick with ${labelOf.get(blocker) || "another choice"}` : ""
      })
    })
  }

  add() {
    const blocks = this.persons()
    const last = blocks[blocks.length - 1]
    const next = Math.max(...blocks.map((b) => Number(b.dataset.signupPerson))) + 1
    const old = last.dataset.signupPerson
    const clone = last.cloneNode(true)
    clone.dataset.signupPerson = next
    const fix = (value) => value.replace(new RegExp(`^(f-)?p${old}_`), `$1p${next}_`)
    clone.querySelectorAll("[name], [id], [for]").forEach((el) => {
      if (el.name) el.name = fix(el.name)
      if (el.id) el.id = fix(el.id)
      if (el.htmlFor) el.htmlFor = fix(el.htmlFor)
    })
    clone.querySelectorAll("input, textarea").forEach((el) => {
      if (el.type === "checkbox" || el.type === "radio") el.checked = false
      else el.value = ""
      if (!el.hasAttribute("data-full")) el.disabled = false
    })
    const heading = clone.querySelector("[data-signup-heading]")
    if (heading) heading.textContent = `Person ${blocks.length + 1}`
    last.after(clone)
    if (this.hasCountTarget) this.countTarget.value = next + 1
    this.update()
    clone.querySelector("input[type=text]")?.focus()
  }
}
