/**
 * PTT option arrays aligned with pkg/core/config/pttoptions/pttoptions.go.
 * Used for typed Include/Avoid filters and sort order lists.
 */

export const QualityOptions = [
  'CAM', 'TeleSync', 'TeleCine', 'SCR',
  'WEB', 'WEB-DL', 'WEBRip', 'WEB-DLRip',
  'HDTV', 'HDTVRip', 'PDTV', 'TVRip', 'SATRip',
  'BluRay', 'BluRay REMUX', 'REMUX', 'BRRip', 'BDRip', 'UHDRip', 'HDRip', 'DVD', 'DVDRip', 'PPVRip', 'R5',
  'XviD', 'DivX'
]

export const ResolutionOptions = ['4k', '2160p', '2k', '1440p', '1080p', '720p', '576p', '480p', '360p', '240p']

/** Grouped resolution names for sort (4k, 1080p, 720p, sd) */
export const ResolutionGroupOptions = ['4k', '1080p', '720p', 'sd']

export const CodecOptions = ['AVC', 'HEVC', 'MPEG-2', 'DivX', 'Xvid']

export const AudioOptions = [
  'DTS Lossless', 'DTS Lossy', 'Atmos', 'TrueHD', 'FLAC', 'DDP', 'EAC3', 'DD', 'AC3', 'AAC', 'PCM', 'OPUS', 'HQ', 'MP3'
]

export const ChannelsOptions = ['2.0', '5.1', '7.1', 'stereo', 'mono']

export const BitDepthOptions = ['8bit', '10bit', '12bit']

export const ContainerOptions = ['mkv', 'avi', 'mp4', 'wmv', 'mpg', 'mpeg']

/** HDR / visual tag options */
export const HDROptions = ['DV', 'HDR10+', 'HDR', 'SDR']

export const ThreeDOptions = ['3D', '3D HSBS', '3D SBS', '3D HOU', '3D OU']

/** Language option values stored in config and sent to backend (short codes / special tokens). */
export const LanguageOptions = [
  'multi subs', 'multi audio', 'dual audio',
  'en', 'ja', 'ko', 'zh', 'zh-tw', 'fr', 'es', 'es-419', 'pt', 'it', 'de', 'ru', 'uk', 'nl', 'da', 'fi', 'sv', 'no', 'el', 'lt', 'lv', 'et', 'pl', 'cs', 'sk', 'hu', 'ro', 'bg', 'sr', 'hr', 'sl', 'hi', 'te', 'ta', 'ml', 'kn', 'mr', 'gu', 'pa', 'bn', 'vi', 'id', 'th', 'ms', 'ar', 'tr', 'he', 'fa'
]

/** Map language code (or special token) → full name for UI display. Backend stores codes. */
export const languageCodeToName = {
  'multi subs': 'Multi subs',
  'multi audio': 'Multi audio',
  'dual audio': 'Dual audio',
  en: 'English',
  ja: 'Japanese',
  ko: 'Korean',
  zh: 'Chinese',
  'zh-tw': 'Chinese (Taiwan)',
  fr: 'French',
  es: 'Spanish',
  'es-419': 'Spanish (Latin America)',
  pt: 'Portuguese',
  it: 'Italian',
  de: 'German',
  ru: 'Russian',
  uk: 'Ukrainian',
  nl: 'Dutch',
  da: 'Danish',
  fi: 'Finnish',
  sv: 'Swedish',
  no: 'Norwegian',
  el: 'Greek',
  lt: 'Lithuanian',
  lv: 'Latvian',
  et: 'Estonian',
  pl: 'Polish',
  cs: 'Czech',
  sk: 'Slovak',
  hu: 'Hungarian',
  ro: 'Romanian',
  bg: 'Bulgarian',
  sr: 'Serbian',
  hr: 'Croatian',
  sl: 'Slovenian',
  hi: 'Hindi',
  te: 'Telugu',
  ta: 'Tamil',
  ml: 'Malayalam',
  kn: 'Kannada',
  mr: 'Marathi',
  gu: 'Gujarati',
  pa: 'Punjabi',
  bn: 'Bengali',
  vi: 'Vietnamese',
  id: 'Indonesian',
  th: 'Thai',
  ms: 'Malay',
  ar: 'Arabic',
  tr: 'Turkish',
  he: 'Hebrew',
  fa: 'Persian'
}

export const EditionOptions = [
  "Anniversary Edition", "Ultimate Edition", "Director's Cut", "Extended Edition",
  "Collector's Edition", "Theatrical", "Uncut", "IMAX", "Diamond Edition", "Remastered"
]

export const NetworkOptions = [
  'Apple TV', 'Amazon', 'Netflix', 'Nickelodeon', 'Disney', 'HBO', 'Hulu', 'CBS', 'NBC', 'AMC', 'PBS', 'Crunchyroll', 'VICE', 'Sony', 'Hallmark', 'Adult Swim', 'Animal Planet', 'Cartoon Network'
]

export const RegionOptions = ['R0', 'R1', 'R2', 'R2J', 'R3', 'R4', 'R5', 'R6', 'R7', 'R8', 'R9', 'PAL', 'NTSC', 'SECAM']

/** Display labels for sort list (e.g. 4k -> 4K) */
export const resolutionGroupLabels = {
  '4k': '4K',
  '1080p': '1080p',
  '720p': '720p',
  'sd': 'SD'
}

/**
 * Default sort orders: best quality first, worst last.
 * Used when no saved config; user can still reorder.
 */
export const DefaultResolutionOrder = ['4k', '1080p', '720p', 'sd']
export const DefaultCodecOrder = ['HEVC', 'AVC', 'MPEG-2', 'DivX', 'Xvid']
export const DefaultAudioOrder = ['Atmos', 'TrueHD', 'DTS Lossless', 'DTS Lossy', 'FLAC', 'DDP', 'EAC3', 'DD', 'AC3', 'AAC', 'PCM', 'OPUS', 'HQ', 'MP3']
export const DefaultQualityOrder = [
  'BluRay REMUX', 'REMUX', 'BluRay', 'BRRip', 'BDRip', 'UHDRip', 'HDRip', 'WEB-DL', 'WEBRip', 'WEB-DLRip', 'WEB',
  'HDTV', 'HDTVRip', 'PDTV', 'TVRip', 'SATRip', 'DVD', 'DVDRip', 'PPVRip', 'R5', 'XviD', 'DivX',
  'CAM', 'TeleSync', 'TeleCine', 'SCR'
]
export const DefaultVisualTagOrder = ['DV', 'HDR10+', 'HDR', 'SDR', '3D', '3D HSBS', '3D SBS', '3D HOU', '3D OU']
export const DefaultChannelsOrder = ['7.1', '5.1', '2.0', 'stereo', 'mono']
export const DefaultBitDepthOrder = ['12bit', '10bit', '8bit']
export const DefaultContainerOrder = ['mkv', 'mp4', 'avi', 'wmv', 'mpg', 'mpeg']
/** Short default: only en + multi/dual; user adds more from full list */
export const DefaultLanguagesOrder = ['en', 'multi subs', 'multi audio', 'dual audio']
export const DefaultEditionOrder = []
export const DefaultNetworkOrder = []
export const DefaultRegionOrder = []
export const DefaultThreeDOrder = []
