variable "name" {
  description = "Resource group name."
  type        = string
}

variable "location" {
  description = "Azure region."
  type        = string
}

variable "tags" {
  description = "Tags to apply. Must include auto-destroy and expires-at for the cleanup workflow contract."
  type        = map(string)
}
