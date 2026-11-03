import BoringAvatar from 'boring-avatars'
import { useState } from 'react'

// Mid-tone colors that read on both the light and the dark page.
const PALETTE = ['#264653', '#2a9d8f', '#e9c46a', '#f4a261', '#e76f51']

interface AvatarProps {
  name: string
  picture?: string
}

/** The user's picture, or a colorful avatar generated from the name (same name, same avatar). */
export function Avatar({ name, picture }: AvatarProps) {
  const [failed, setFailed] = useState<string>()
  if (picture && failed !== picture) {
    return <img className="avatar" src={picture} alt={name} referrerPolicy="no-referrer" onError={() => setFailed(picture)} />
  }
  return <BoringAvatar className="avatar" name={name} variant="beam" colors={PALETTE} size="100%" title aria-label={name} />
}
